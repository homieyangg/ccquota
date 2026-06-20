// Package ingest 提供 OTLP HTTP metrics 接收端，將 claude_code.cost.usage
// 與 claude_code.token.usage 寫入 store。
package ingest

import (
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ccquota/ccquota/internal/store"
)

const (
	metricCost   = "claude_code.cost.usage"
	metricTokens = "claude_code.token.usage"

	attrAccount = "ccquota.account"
	attrUser    = "ccquota.user"
	attrUserID  = "user.id"
)

type handler struct {
	s     *store.Store
	token []byte // 空表示 ingest 停用
}

// New 回傳一個 http.Handler，掛在 POST /v1/metrics。
// token 為空字串時拒絕所有請求（ingest 停用）。
func New(s *store.Store, token string) http.Handler {
	return &handler{s: s, token: []byte(token)}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if len(h.token) == 0 {
		http.Error(w, "ingest disabled", http.StatusUnauthorized)
		return
	}
	if !bearerOK(h.token, r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, useJSON, err := readBody(r)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	req := &collectorpb.ExportMetricsServiceRequest{}
	if useJSON {
		if err := protojson.Unmarshal(body, req); err != nil {
			http.Error(w, "json unmarshal: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		if err := proto.Unmarshal(body, req); err != nil {
			http.Error(w, "proto unmarshal: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	h.process(req)

	resp := &collectorpb.ExportMetricsServiceResponse{}
	if useJSON {
		data, _ := protojson.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	} else {
		data, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}
}

// readBody 讀取並解壓 body，回傳 (bytes, isJSON, err)。
// 最多讀取 10 MB（含解壓後），防止 gzip-bomb。
func readBody(r *http.Request) ([]byte, bool, error) {
	const maxBytes = 10 << 20 // 10 MB
	var reader io.Reader = io.LimitReader(r.Body, maxBytes)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, false, err
		}
		defer gz.Close()
		reader = io.LimitReader(gz, maxBytes)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, err
	}
	ct := r.Header.Get("Content-Type")
	isJSON := ct == "application/json"
	return data, isJSON, nil
}

// process 遍歷 ResourceMetrics，解析 account/user 屬性與兩個 metrics，寫入 store。
func (h *handler) process(req *collectorpb.ExportMetricsServiceRequest) {
	now := time.Now().Unix()
	for _, rm := range req.GetResourceMetrics() {
		account, userName, userID, ok := extractAccountUser(rm.GetResource().GetAttributes())
		if !ok {
			continue
		}

		// 解析 display name：mapping > ccquota.user > skip
		user := userName
		if userID != "" {
			if mapped, found, err := h.s.ResolveUserID(userID); err == nil && found {
				user = mapped
			} else if userName != "" {
				h.s.LearnUserMapping(userID, userName)
			}
		}
		if user == "" {
			continue
		}
		var totalCost float64
		var totalTokens int64

		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				switch m.GetName() {
				case metricCost:
					totalCost += sumDataPoints(m)
				case metricTokens:
					totalTokens += int64(sumDataPoints(m))
				}
			}
		}

		ts := dataPointTime(rm)
		if ts == 0 {
			ts = now
		}

		if err := h.s.InsertUserCost(account, user, ts, totalCost, totalTokens); err != nil {
			log.Printf("ingest: InsertUserCost account=%s user=%s err=%v", account, user, err)
		}
	}
}

// extractAccountUser 從 resource attributes 讀取 ccquota.account、ccquota.user 和 user.id。
func extractAccountUser(attrs []*commonpb.KeyValue) (account, user, userID string, ok bool) {
	for _, kv := range attrs {
		switch kv.GetKey() {
		case attrAccount:
			account = kv.GetValue().GetStringValue()
		case attrUser:
			user = kv.GetValue().GetStringValue()
		case attrUserID:
			userID = kv.GetValue().GetStringValue()
		}
	}
	ok = account != ""
	return
}

// sumDataPoints 對 Sum metric 的 NumberDataPoint 求和（delta 模式下即本次增量）。
func sumDataPoints(m *metricspb.Metric) float64 {
	s := m.GetSum()
	if s == nil {
		return 0
	}
	var total float64
	for _, dp := range s.GetDataPoints() {
		switch v := dp.GetValue().(type) {
		case *metricspb.NumberDataPoint_AsDouble:
			total += v.AsDouble
		case *metricspb.NumberDataPoint_AsInt:
			total += float64(v.AsInt)
		}
	}
	return total
}

// dataPointTime 取第一個 ResourceMetrics 下第一個可用 data point 的時間戳（秒）。
func dataPointTime(rm *metricspb.ResourceMetrics) int64 {
	for _, sm := range rm.GetScopeMetrics() {
		for _, m := range sm.GetMetrics() {
			if s := m.GetSum(); s != nil {
				for _, dp := range s.GetDataPoints() {
					if dp.GetTimeUnixNano() != 0 {
						return int64(dp.GetTimeUnixNano() / 1e9)
					}
				}
			}
		}
	}
	return 0
}

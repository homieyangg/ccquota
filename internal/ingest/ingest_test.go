package ingest

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/ccquota/ccquota/internal/store"
)

const testToken = "test-secret"

// buildRequest 組裝一個包含 ccquota.account / ccquota.user 的 ExportMetricsServiceRequest。
func buildRequest(account, user string, cost float64, tokens int64) *collectorpb.ExportMetricsServiceRequest {
	tsNano := uint64(1_700_000_000 * 1e9)
	return &collectorpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: attrAccount, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: account}}},
						{Key: attrUser, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: user}}},
					},
				},
				ScopeMetrics: []*metricspb.ScopeMetrics{
					{
						Metrics: []*metricspb.Metric{
							{
								Name: metricCost,
								Data: &metricspb.Metric_Sum{
									Sum: &metricspb.Sum{
										DataPoints: []*metricspb.NumberDataPoint{
											{
												TimeUnixNano: tsNano,
												Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: cost},
											},
										},
									},
								},
							},
							{
								Name: metricTokens,
								Data: &metricspb.Metric_Sum{
									Sum: &metricspb.Sum{
										DataPoints: []*metricspb.NumberDataPoint{
											{
												TimeUnixNano: tsNano,
												Value:        &metricspb.NumberDataPoint_AsInt{AsInt: tokens},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func postProto(t *testing.T, h http.Handler, token string, req *collectorpb.ExportMetricsServiceRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/x-protobuf")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestIngestHappyPath 驗證正常匯入後 store 有正確的 cost / tokens。
func TestIngestHappyPath(t *testing.T) {
	s := openStore(t)
	h := New(s, testToken)

	req := buildRequest("acct1", "alice", 0.05, 500)
	w := postProto(t, h, testToken, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	costs, err := s.UserPeriodCosts("acct1", 0)
	if err != nil {
		t.Fatal(err)
	}
	uc, ok := costs["alice"]
	if !ok {
		t.Fatal("alice not found in store")
	}
	if uc.Tokens != 500 {
		t.Fatalf("tokens: want 500, got %d", uc.Tokens)
	}
	wantCost := 0.05
	if uc.Cost < wantCost-1e-9 || uc.Cost > wantCost+1e-9 {
		t.Fatalf("cost: want %.4f, got %.4f", wantCost, uc.Cost)
	}
}

// TestIngestWrongBearer 驗證 token 不符時回傳 401。
func TestIngestWrongBearer(t *testing.T) {
	s := openStore(t)
	h := New(s, testToken)

	req := buildRequest("acct1", "alice", 0.01, 100)
	w := postProto(t, h, "wrong-token", req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// TestIngestMissingBearer 驗證無 Authorization header 時回傳 401。
func TestIngestMissingBearer(t *testing.T) {
	s := openStore(t)
	h := New(s, testToken)

	req := buildRequest("acct1", "alice", 0.01, 100)
	w := postProto(t, h, "" /* no token */, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// TestIngestEmptyToken 驗證 server token 為空時（ingest 停用）回傳 401。
func TestIngestEmptyToken(t *testing.T) {
	s := openStore(t)
	h := New(s, "") // 停用

	req := buildRequest("acct1", "alice", 0.01, 100)
	w := postProto(t, h, "anything", req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// TestIngestMissingUser 驗證 resource 缺 ccquota.user 時不寫入 store。
func TestIngestMissingUser(t *testing.T) {
	s := openStore(t)
	h := New(s, testToken)

	// 故意省略 ccquota.user
	noUserReq := &collectorpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: attrAccount, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "acct1"}}},
						// 故意不加 ccquota.user
					},
				},
				ScopeMetrics: []*metricspb.ScopeMetrics{
					{
						Metrics: []*metricspb.Metric{
							{
								Name: metricCost,
								Data: &metricspb.Metric_Sum{
									Sum: &metricspb.Sum{
										DataPoints: []*metricspb.NumberDataPoint{
											{Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 9.99}},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	w := postProto(t, h, testToken, noUserReq)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	// store 應該沒有任何紀錄
	costs, err := s.UserPeriodCosts("acct1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) != 0 {
		t.Fatalf("want 0 rows, got %d: %v", len(costs), costs)
	}
}

// TestIngestGzip 驗證 Content-Encoding: gzip 的請求可正常解壓並處理。
func TestIngestGzip(t *testing.T) {
	s := openStore(t)
	h := New(s, testToken)

	req := buildRequest("acct1", "bob", 0.10, 1000)
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write(data)
	gz.Close()

	r := httptest.NewRequest(http.MethodPost, "/v1/metrics", &buf)
	r.Header.Set("Content-Type", "application/x-protobuf")
	r.Header.Set("Content-Encoding", "gzip")
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	costs, err := s.UserPeriodCosts("acct1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if costs["bob"].Tokens != 1000 {
		t.Fatalf("bob tokens: want 1000, got %d", costs["bob"].Tokens)
	}
}

// TestIngestMultipleResources 驗證多個 resource（不同 user）都寫入。
func TestIngestMultipleResources(t *testing.T) {
	s := openStore(t)
	h := New(s, testToken)

	tsNano := uint64(1_700_000_000 * 1e9)
	multi := &collectorpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			resourceFor("acct1", "carol", 0.02, 200, tsNano),
			resourceFor("acct1", "dave", 0.08, 800, tsNano),
		},
	}
	w := postProto(t, h, testToken, multi)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	costs, err := s.UserPeriodCosts("acct1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) != 2 {
		t.Fatalf("want 2 users, got %d", len(costs))
	}
	if costs["carol"].Tokens != 200 {
		t.Fatalf("carol tokens: want 200, got %d", costs["carol"].Tokens)
	}
	if costs["dave"].Tokens != 800 {
		t.Fatalf("dave tokens: want 800, got %d", costs["dave"].Tokens)
	}
}

func resourceFor(account, user string, cost float64, tokens int64, tsNano uint64) *metricspb.ResourceMetrics {
	return &metricspb.ResourceMetrics{
		Resource: &resourcepb.Resource{
			Attributes: []*commonpb.KeyValue{
				{Key: attrAccount, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: account}}},
				{Key: attrUser, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: user}}},
			},
		},
		ScopeMetrics: []*metricspb.ScopeMetrics{
			{
				Metrics: []*metricspb.Metric{
					{
						Name: metricCost,
						Data: &metricspb.Metric_Sum{
							Sum: &metricspb.Sum{
								DataPoints: []*metricspb.NumberDataPoint{
									{TimeUnixNano: tsNano, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: cost}},
								},
							},
						},
					},
					{
						Name: metricTokens,
						Data: &metricspb.Metric_Sum{
							Sum: &metricspb.Sum{
								DataPoints: []*metricspb.NumberDataPoint{
									{TimeUnixNano: tsNano, Value: &metricspb.NumberDataPoint_AsInt{AsInt: tokens}},
								},
							},
						},
					},
				},
			},
		},
	}
}

package ingest

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/ccquota/ccquota/internal/poller"
	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
)

// 重置偵測門檻，與 poller cycle 內保持一致。
const (
	resetDropPct  = 5
	resetMinAdvSc = 3600
)

// NewUsageHandler 接收外部推送的 7d/5h 用量：POST /v1/usage?account=<id>，
// body 為 Anthropic /api/oauth/usage 的原始 JSON。設計給「在有真 Claude Code
// 登入的機器上讀現成 token、打 usage endpoint」的 client 推回來用，server 本身
// 完全不碰被限流的 token endpoint。
//
// onReset 在偵測到額度重置時呼叫（傳 nil 則不通知）。token 為空字串時拒絕所有請求。
func NewUsageHandler(s *store.Store, token string, onReset func(accountID string, prev, cur float64)) http.Handler {
	tok := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(tok) == 0 {
			http.Error(w, "ingest disabled", http.StatusUnauthorized)
			return
		}
		const prefix = "Bearer "
		bearer := r.Header.Get("Authorization")
		if len(bearer) <= len(prefix) ||
			subtle.ConstantTimeCompare([]byte(bearer[len(prefix):]), tok) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		account := r.URL.Query().Get("account")
		if account == "" {
			http.Error(w, "missing account", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		snap, err := usage.Parse(body)
		if err != nil {
			http.Error(w, "bad usage json", http.StatusBadRequest)
			return
		}

		now := time.Now().Unix()
		prev, hadPrev, _ := s.LatestReading(account)
		if err := s.InsertReading(store.Reading{
			AccountID: account, TS: now,
			SevenDay: snap.SevenDay, FiveHour: snap.FiveHour, Sonnet: snap.Sonnet, Opus: snap.Opus,
			SevenDayResetsAt: snap.SevenDayResetsAt, FiveHourResetsAt: snap.FiveHourResetsAt,
		}); err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}

		if hadPrev && poller.DetectReset(prev.SevenDay, prev.SevenDayResetsAt,
			snap.SevenDay, snap.SevenDayResetsAt, resetDropPct, resetMinAdvSc) {
			detail, _ := json.Marshal(map[string]float64{"from": prev.SevenDay, "to": snap.SevenDay})
			_ = s.InsertEvent(account, now, "reset", string(detail))
			if onReset != nil {
				onReset(account, prev.SevenDay, snap.SevenDay)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

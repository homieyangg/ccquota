package ingest

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ccquota/ccquota/internal/share"
	"github.com/ccquota/ccquota/internal/store"
)

type quotaThresholds struct {
	FiveHourCrit  float64 `json:"five_hour_crit"`
	SevenDayWarn  float64 `json:"seven_day_warn"`
	SevenDayCrit  float64 `json:"seven_day_crit"`
	UserShareWarn float64 `json:"user_share_warn"`
	UserShareCrit float64 `json:"user_share_crit"`
}

type quotaResp struct {
	FiveHour   *float64        `json:"five_hour"`
	SevenDay   *float64        `json:"seven_day"`
	SharePct   *float64        `json:"share_pct"`
	Stale      bool            `json:"stale"`
	Thresholds quotaThresholds `json:"thresholds"`
}

// NewQuotaHandler 回傳 client statusline 用的額度查詢端點:GET /v1/quota?account=<id>&user=<name>。
// 認證同其他 /v1 端點(ingest Bearer token)。回帳號級 5h/7d 使用率、該 user 的平分額度佔比、告警門檻。
// user 省略時不算 share_pct;帳號無 reading 時 5h/7d 回 null。
func NewQuotaHandler(s *store.Store, staleSec int64, token string) http.Handler {
	tok := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !bearerOK(tok, r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		account := r.URL.Query().Get("account")
		if account == "" {
			http.Error(w, "missing account", http.StatusBadRequest)
			return
		}
		user := r.URL.Query().Get("user")

		reading, ok, err := s.LatestReading(account)
		if err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}
		th, _ := s.GetAlertThresholds()
		resp := quotaResp{Thresholds: quotaThresholds{
			FiveHourCrit:  th.FiveHourCrit,
			SevenDayWarn:  th.SevenDayWarn,
			SevenDayCrit:  th.SevenDayCrit,
			UserShareWarn: th.UserShareWarn,
			UserShareCrit: th.UserShareCrit,
		}}

		if ok {
			now := time.Now().Unix()
			fh, sd := reading.FiveHour, reading.SevenDay
			resp.FiveHour = &fh
			resp.SevenDay = &sd
			staleThresh := staleSec
			if staleThresh <= 0 {
				staleThresh = 1800
			}
			staleData := now-reading.TS > staleThresh
			pastReset := reading.SevenDayResetsAt > 0 && now > reading.SevenDayResetsAt
			resp.Stale = staleData || pastReset

			if user != "" {
				sinceTS := share.SinceTS(reading, ok, now)
				baseline, _ := s.BudgetHWM(account)
				if res, err := share.Compute(s, account, sinceTS, reading.SevenDay, baseline); err == nil {
					for _, sh := range res.Shares {
						if sh.User == user {
							p := sh.SharePct
							resp.SharePct = &p
							break
						}
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
}

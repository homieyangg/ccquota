package ingest

import (
	"encoding/json"
	"net/http"

	"github.com/ccquota/ccquota/internal/store"
)

type backfillReq struct {
	Account     string `json:"account"`
	User        string `json:"user"`
	Tokens      int64  `json:"tokens"`
	WindowStart int64  `json:"window_start"`
	Cutoff      int64  `json:"cutoff"` // client 算到哪一刻為止;伺服器只記 tokens,僅供除錯
}

// NewBackfillHandler 回傳 client 回填端點:POST /v1/backfill,認證同其他 /v1(ingest Bearer)。
// 全新安裝時 client 掃本機歷史 token 推上來,讓 Day 1 就能反推 token 週額度。同 (account,user) 冪等取代。
func NewBackfillHandler(s *store.Store, token string) http.Handler {
	tok := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !bearerOK(tok, r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req backfillReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Account == "" || req.User == "" {
			http.Error(w, "missing account/user", http.StatusBadRequest)
			return
		}
		if req.Tokens <= 0 || req.WindowStart <= 0 {
			http.Error(w, "missing tokens/window_start", http.StatusBadRequest)
			return
		}
		if err := s.InsertBackfill(req.Account, req.User, req.Tokens, req.WindowStart); err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
}

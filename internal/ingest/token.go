package ingest

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/ccquota/ccquota/internal/store"
)

type tokenPush struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   int64  `json:"expires_at"` // unix seconds, 選填
}

// NewTokenHandler 接收 client 推來的「現成 access token」:POST /v1/token?account=<id>。
// 設計給「在有真 Claude Code 登入的機器上讀本機現成 token」的 client 定時回傳,server
// 再用它統一輪詢 usage(每帳號每週期只打一次,天生不會 N 倍 429),且永不碰 token endpoint。
//
// 只更新 access token / expires_at,保留既有 refresh token。帳號不存在則自動建立。
func NewTokenHandler(s *store.Store, token string) http.Handler {
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
		account := r.URL.Query().Get("account")
		if account == "" {
			http.Error(w, "missing account", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		var p tokenPush
		if err := json.Unmarshal(body, &p); err != nil || p.AccessToken == "" {
			http.Error(w, "bad token json", http.StatusBadRequest)
			return
		}

		// 取既有帳號(保留 label / refresh token),不存在則新建。
		a, err := s.GetAccount(account)
		if err != nil {
			a = store.Account{ID: account, Label: account}
		}
		a.AccessToken = p.AccessToken
		if p.ExpiresAt > 0 {
			a.ExpiresAt = p.ExpiresAt
		}
		if err := s.UpsertAccount(a); err != nil {
			http.Error(w, "store error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

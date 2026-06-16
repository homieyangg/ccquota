package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ccquota/ccquota/internal/store"
)

func postToken(t *testing.T, h http.Handler, account, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/token?account="+account, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestTokenHandlerUpserts(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewTokenHandler(s, "secret")

	rr := postToken(t, h, "main", "Bearer secret", `{"access_token":"AT-123","expires_at":99999}`)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rr.Code, rr.Body.String())
	}
	a, err := s.GetAccount("main")
	if err != nil {
		t.Fatalf("帳號應被建立: %v", err)
	}
	if a.AccessToken != "AT-123" || a.ExpiresAt != 99999 {
		t.Errorf("token/expiry 沒存對: %q %d", a.AccessToken, a.ExpiresAt)
	}
}

// TestTokenHandlerKeepsRefresh:推 token 不可清掉既有 refresh token。
func TestTokenHandlerKeepsRefresh(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	_ = s.UpsertAccount(store.Account{ID: "main", Label: "main", AccessToken: "old", RefreshToken: "RT", ExpiresAt: 1})
	h := NewTokenHandler(s, "secret")

	postToken(t, h, "main", "Bearer secret", `{"access_token":"new"}`)
	a, _ := s.GetAccount("main")
	if a.AccessToken != "new" {
		t.Errorf("access token 應更新為 new,得 %q", a.AccessToken)
	}
	if a.RefreshToken != "RT" {
		t.Errorf("refresh token 應保留 RT,得 %q", a.RefreshToken)
	}
}

func TestTokenHandlerRejects(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewTokenHandler(s, "secret")

	if rr := postToken(t, h, "main", "Bearer wrong", `{"access_token":"x"}`); rr.Code != http.StatusUnauthorized {
		t.Errorf("錯 token 應 401,得 %d", rr.Code)
	}
	if rr := postToken(t, h, "", "Bearer secret", `{"access_token":"x"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("缺 account 應 400,得 %d", rr.Code)
	}
	if rr := postToken(t, h, "main", "Bearer secret", `{}`); rr.Code != http.StatusBadRequest {
		t.Errorf("缺 access_token 應 400,得 %d", rr.Code)
	}
}

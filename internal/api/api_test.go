package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func authedGet(t *testing.T, handler http.Handler, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetBasicAuth("admin", AdminPassword)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Result()
}

func TestAccountsEmpty(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800)
	resp := authedGet(t, h, "/api/accounts")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestAccountsWithReading(t *testing.T) {
	s := testStore(t)
	_ = s.UpsertAccount(store.Account{ID: "main", Label: "Main", AccessToken: "a", RefreshToken: "r", ExpiresAt: 9999})
	_ = s.InsertReading(store.Reading{AccountID: "main", TS: time.Now().Unix(), SevenDay: 42, FiveHour: 10})
	h := New(s, &oauth.Client{}, 1800)
	resp := authedGet(t, h, "/api/accounts")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out) != 1 {
		t.Fatalf("want 1 account, got %d", len(out))
	}
	if out[0]["id"] != "main" {
		t.Fatalf("wrong id: %v", out[0]["id"])
	}
	if out[0]["has_reading"] != true {
		t.Fatalf("expected has_reading=true: %v", out[0])
	}
	// cost object 必須存在。
	if out[0]["cost"] == nil {
		t.Fatalf("expected cost object, got nil: %v", out[0])
	}
}

func TestAccountsCostCalc(t *testing.T) {
	s := testStore(t)
	_ = s.UpsertAccount(store.Account{ID: "acct1", Label: "Test", AccessToken: "a", RefreshToken: "r", ExpiresAt: 9999})

	now := time.Now().Unix()
	// seven_day_resets_at = now + 3 天（模擬 3 天後重置）
	// 期間起始 = resets_at - 7天 = now - 4天
	resetsAt := now + 3*24*3600
	_ = s.InsertReading(store.Reading{
		AccountID:        "acct1",
		TS:               now,
		SevenDay:         50, // 50% 用量
		FiveHour:         20,
		SevenDayResetsAt: resetsAt,
	})

	// 插入兩個使用者的成本（ts 落在期間內）
	sinceTS := resetsAt - 7*24*3600 // = now - 4天
	_ = s.InsertUserCost("acct1", "alice", sinceTS+10, 30.0, 1000)
	_ = s.InsertUserCost("acct1", "bob", sinceTS+20, 20.0, 500)

	h := New(s, &oauth.Client{}, 1800)
	resp := authedGet(t, h, "/api/accounts")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out) != 1 {
		t.Fatalf("want 1 account, got %d", len(out))
	}

	cost, ok := out[0]["cost"].(map[string]any)
	if !ok {
		t.Fatalf("cost field missing or wrong type: %v", out[0]["cost"])
	}

	// period_cost_usd = 30 + 20 = 50
	periodCost, _ := cost["period_cost_usd"].(float64)
	if periodCost != 50.0 {
		t.Errorf("period_cost_usd: want 50, got %v", periodCost)
	}

	// weekly_budget_usd = 50 / 0.50 = 100
	weeklyBudget, _ := cost["weekly_budget_usd"].(float64)
	if weeklyBudget != 100.0 {
		t.Errorf("weekly_budget_usd: want 100, got %v", weeklyBudget)
	}

	// per_user_budget_usd = 100 / 2 = 50
	perUserBudget, _ := cost["per_user_budget_usd"].(float64)
	if perUserBudget != 50.0 {
		t.Errorf("per_user_budget_usd: want 50, got %v", perUserBudget)
	}

	// users: alice 30 USD (60%), bob 20 USD (40%)，依 cost 降序
	users, _ := cost["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("want 2 users, got %d", len(users))
	}
	alice, _ := users[0].(map[string]any)
	if alice["user"] != "alice" {
		t.Errorf("first user should be alice (highest cost), got %v", alice["user"])
	}
	aliceShare, _ := alice["share_pct"].(float64)
	if aliceShare != 60.0 {
		t.Errorf("alice share_pct: want 60, got %v", aliceShare)
	}
}

func TestAuthRejectsNoCredentials(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800)
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuthRejectsWrongPassword(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800)
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.SetBasicAuth("admin", "wrongpassword")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestHealthz(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestLoginStartComplete(t *testing.T) {
	// Fake token server
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "authorization_code" || body["code"] != "testcode" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT", "refresh_token": "RT", "expires_in": 28800,
		})
	}))
	defer tokenSrv.Close()

	s := testStore(t)
	oc := &oauth.Client{HTTP: tokenSrv.Client(), TokenURL: tokenSrv.URL}
	h := New(s, oc, 1800)

	// Start login
	startBody, _ := json.Marshal(map[string]string{"label": "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/login/start", bytes.NewReader(startBody))
	req.SetBasicAuth("admin", AdminPassword)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login/start want 200, got %d: %s", w.Code, w.Body.String())
	}
	var startResp map[string]string
	json.NewDecoder(w.Body).Decode(&startResp)
	loginID := startResp["login_id"]
	if loginID == "" {
		t.Fatal("no login_id in response")
	}
	if !strings.Contains(startResp["authorize_url"], "claude.com") {
		t.Fatalf("bad authorize_url: %s", startResp["authorize_url"])
	}

	// Complete login
	completeBody, _ := json.Marshal(map[string]string{
		"login_id": loginID,
		"id":       "newacct",
		"label":    "New Account",
		"code":     "testcode",
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/login/complete", bytes.NewReader(completeBody))
	req2.SetBasicAuth("admin", AdminPassword)
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("login/complete want 200, got %d: %s", w2.Code, w2.Body.String())
	}

	// Verify account was stored
	acct, err := s.GetAccount("newacct")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acct.AccessToken != "AT" {
		t.Fatalf("wrong access token: %q", acct.AccessToken)
	}

	// Verify pending entry was cleaned up (using same login_id again should fail)
	req3 := httptest.NewRequest(http.MethodPost, "/api/login/complete", bytes.NewReader(completeBody))
	req3.SetBasicAuth("admin", AdminPassword)
	req3.Header.Set("Content-Type", "application/json")
	// Re-encode body since body was consumed
	completeBody2, _ := json.Marshal(map[string]string{
		"login_id": loginID, "id": "newacct2", "label": "New2", "code": "testcode",
	})
	req3 = httptest.NewRequest(http.MethodPost, "/api/login/complete", bytes.NewReader(completeBody2))
	req3.SetBasicAuth("admin", AdminPassword)
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("second complete should fail with 400, got %d", w3.Code)
	}

	_ = context.Background() // suppress import
}

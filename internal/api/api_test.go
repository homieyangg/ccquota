package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/secret"
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

func testCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	c, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
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
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	resp := authedGet(t, h, "/api/accounts")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestAccountsWithReading(t *testing.T) {
	s := testStore(t)
	_ = s.UpsertAccount(store.Account{ID: "main", Label: "Main", AccessToken: "a", RefreshToken: "r", ExpiresAt: 9999})
	_ = s.InsertReading(store.Reading{AccountID: "main", TS: time.Now().Unix(), SevenDay: 42, FiveHour: 10})
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
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

	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
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

// TestAccountsCostKeepsRecentRoster:週限重置後,當前視窗內沒花錢但最近 7 天用過的人
// 仍要留在名單(顯示 $0 / 0%),且不稀釋 per_user_budget(分母只算當前視窗有花錢的人)。
func TestAccountsCostKeepsRecentRoster(t *testing.T) {
	s := testStore(t)
	_ = s.UpsertAccount(store.Account{ID: "acct1", Label: "Test", AccessToken: "a", RefreshToken: "r", ExpiresAt: 9999})

	now := time.Now().Unix()
	resetsAt := now + 3*24*3600    // 期間起始 = resets_at - 7天 = now - 4天
	_ = s.InsertReading(store.Reading{
		AccountID: "acct1", TS: now, SevenDay: 50, FiveHour: 20, SevenDayResetsAt: resetsAt,
	})

	sinceTS := resetsAt - 7*24*3600
	_ = s.InsertUserCost("acct1", "alice", sinceTS+10, 30.0, 1000) // 當前視窗內,有花錢
	_ = s.InsertUserCost("acct1", "carol", now-5*24*3600, 99.0, 9) // 最近 7 天內、但早於當前視窗 → roster 但 $0
	_ = s.InsertUserCost("acct1", "dave", now-10*24*3600, 99.0, 9) // 超過 7 天 → 不該出現

	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	resp := authedGet(t, h, "/api/accounts")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	cost, _ := out[0]["cost"].(map[string]any)

	users, _ := cost["users"].([]any)
	got := map[string]float64{}
	for _, u := range users {
		um := u.(map[string]any)
		got[um["user"].(string)] = um["cost_usd"].(float64)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 users (alice+carol), got %d: %v", len(got), got)
	}
	if got["alice"] != 30.0 {
		t.Errorf("alice cost: want 30, got %v", got["alice"])
	}
	if c, ok := got["carol"]; !ok || c != 0.0 {
		t.Errorf("carol should be present at $0, got %v (present=%v)", c, ok)
	}
	if _, ok := got["dave"]; ok {
		t.Errorf("dave (>7d) should not appear")
	}

	// per_user_budget 分母只算 alice(有花錢),不被 carol 稀釋:60/1 = 60。
	perUserBudget, _ := cost["per_user_budget_usd"].(float64)
	if perUserBudget != 60.0 {
		t.Errorf("per_user_budget_usd: want 60 (not diluted by carol), got %v", perUserBudget)
	}
}

// TestAccountsCostUsesBaseline:週初 7d% < MinPct(原始反推=0)時,dashboard 的週額度退回
// hwm baseline,並回傳 last_week_budget_usd。證明 baseline 有餵進計算、不再整段空白。
func TestAccountsCostUsesBaseline(t *testing.T) {
	s := testStore(t)
	_ = s.UpsertAccount(store.Account{ID: "acct1", Label: "Test", AccessToken: "a", RefreshToken: "r", ExpiresAt: 9999})

	now := time.Now().Unix()
	resetsAt := now + 3*24*3600
	_ = s.InsertReading(store.Reading{AccountID: "acct1", TS: now, SevenDay: 3, FiveHour: 10, SevenDayResetsAt: resetsAt})

	sinceTS := resetsAt - 7*24*3600
	_ = s.InsertUserCost("acct1", "alice", sinceTS+10, 12.0, 1000)
	_ = s.SetBudgetHWM("acct1", 500, 96)
	_ = s.SetLastWeekBudget("acct1", 480)

	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	resp := authedGet(t, h, "/api/accounts")
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	cost, _ := out[0]["cost"].(map[string]any)

	if wb, _ := cost["weekly_budget_usd"].(float64); wb != 500 {
		t.Errorf("週初應退回 baseline 500,得 %v", wb)
	}
	if lw, _ := cost["last_week_budget_usd"].(float64); lw != 480 {
		t.Errorf("last_week_budget_usd 應 480,得 %v", lw)
	}
	// per_user_budget = 500 / 1 user = 500;alice 12/500 = 2.4%
	if pu, _ := cost["per_user_budget_usd"].(float64); pu != 500 {
		t.Errorf("per_user_budget 應 500,得 %v", pu)
	}
}

func TestAuthRejectsNoCredentials(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuthRejectsWrongPassword(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
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
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
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
	h := New(s, oc, 1800, "", "", testCipher(t), "dev")

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

func TestEnrollNoAdmin(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "itok", "https://demo.example", testCipher(t), "dev")
	req := httptest.NewRequest(http.MethodPost, "/api/enroll",
		strings.NewReader(`{"account":"main","user":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestEnrollSuccess(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "itok", "https://demo.example", testCipher(t), "dev")
	body, _ := json.Marshal(map[string]string{"account": "main", "user": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/enroll", bytes.NewReader(body))
	req.SetBasicAuth("admin", AdminPassword)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	url, _ := resp["url"].(string)
	if !strings.Contains(url, "/e/") {
		t.Fatalf("url should contain /e/, got %q", url)
	}
}

func TestEnrollNoIngestToken(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "https://demo.example", testCipher(t), "dev")
	body, _ := json.Marshal(map[string]string{"account": "main", "user": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/enroll", bytes.NewReader(body))
	req.SetBasicAuth("admin", AdminPassword)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestEnrollScriptValid(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "itok", "https://demo.example", testCipher(t), "dev")

	// 先建立 enrollment
	body, _ := json.Marshal(map[string]string{"account": "main", "user": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/enroll", bytes.NewReader(body))
	req.SetBasicAuth("admin", AdminPassword)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enroll want 200, got %d", w.Code)
	}
	var enrollResp map[string]any
	json.NewDecoder(w.Body).Decode(&enrollResp)
	url, _ := enrollResp["url"].(string)
	// url = https://demo.example/e/<token>
	parts := strings.Split(url, "/e/")
	if len(parts) != 2 {
		t.Fatalf("unexpected url format: %q", url)
	}
	token := parts[1]

	// 取 script（不帶 admin auth）
	req2 := httptest.NewRequest(http.MethodGet, "/e/"+token, nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("script want 200, got %d", w2.Code)
	}
	script := w2.Body.String()
	if !strings.Contains(script, "main") {
		t.Error("script should contain account 'main'")
	}
	if !strings.Contains(script, "alice") {
		t.Error("script should contain user 'alice'")
	}
	if !strings.Contains(script, "itok") {
		t.Error("script should contain ingest token")
	}
	if !strings.Contains(script, "https://demo.example") {
		t.Error("script should contain endpoint")
	}
}

func TestEnrollScriptBadToken(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "itok", "https://demo.example", testCipher(t), "dev")
	req := httptest.NewRequest(http.MethodGet, "/e/badtoken", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// ── 新的 session auth 測試 ──────────────────────────────────────────────────

// loginAndGetCookie 呼叫 /api/auth/login 並回傳 Set-Cookie 值。
func loginAndGetCookie(t *testing.T, h http.Handler, password string) (*http.Cookie, int) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	resp := w.Result()
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c, resp.StatusCode
		}
	}
	return nil, resp.StatusCode
}

// TestAuthStatusOpen 確認 /api/auth/status 在無認證時仍回傳 200 (authed:false)。
func TestAuthStatusOpen(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]bool
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["authed"] {
		t.Fatal("want authed=false, got true")
	}
}

// TestAuthStatusWithBasicAuth 確認 Basic Auth 也讓 status 回傳 authed:true。
func TestAuthStatusWithBasicAuth(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.SetBasicAuth("admin", AdminPassword)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]bool
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp["authed"] {
		t.Fatal("want authed=true with basic auth")
	}
}

// TestLoginSetsSessionCookie 確認正確密碼登入後會拿到 session cookie。
func TestLoginSetsSessionCookie(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	cookie, code := loginAndGetCookie(t, h, AdminPassword)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if cookie == nil {
		t.Fatal("no session cookie in response")
	}
	if cookie.HttpOnly != true {
		t.Error("cookie should be HttpOnly")
	}
}

// TestCookieGrantsAccess 確認帶著合法 session cookie 可通過認證 gate。
func TestCookieGrantsAccess(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")

	// 先登入拿 cookie
	cookie, code := loginAndGetCookie(t, h, AdminPassword)
	if code != http.StatusOK {
		t.Fatalf("login want 200, got %d", code)
	}

	// 用 cookie 打 /api/accounts
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 with cookie, got %d", w.Code)
	}
}

// TestWrongPasswordReturns401 確認密碼錯誤回傳 401。
func TestWrongPasswordReturns401(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	_, code := loginAndGetCookie(t, h, "wrongpassword")
	if code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
}

// TestBasicAuthStillWorks 確認 Basic Auth 仍可通過認證 gate（向後相容）。
func TestBasicAuthStillWorks(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.SetBasicAuth("admin", AdminPassword)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 with basic auth, got %d", w.Code)
	}
}

// TestAuthStatusMustChange 確認 must_change 在 status 回傳中正確呈現。
func TestAuthStatusMustChange(t *testing.T) {
	s := testStore(t)
	// bootstrap 設 must_change=1（auto-gen 情境：不設 CCQUOTA_ADMIN_PASSWORD）
	os.Unsetenv("CCQUOTA_ADMIN_PASSWORD")
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")

	// 未認證時 must_change 不應為 true
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["must_change"].(bool) {
		t.Fatal("want must_change=false when not authed")
	}

	// 登入後 must_change 應為 true（auto-gen 第一次登入）
	cookie, code := loginAndGetCookie(t, h, AdminPassword)
	if code != http.StatusOK {
		t.Fatalf("login want 200, got %d", code)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req2.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	var resp2 map[string]any
	json.NewDecoder(w2.Body).Decode(&resp2)
	if !resp2["authed"].(bool) {
		t.Fatal("want authed=true")
	}
	if !resp2["must_change"].(bool) {
		t.Fatal("want must_change=true for auto-gen password")
	}
}

// TestChangePasswordHappy 確認正確流程可以改密碼並清除 must_change。
func TestChangePasswordHappy(t *testing.T) {
	s := testStore(t)
	os.Unsetenv("CCQUOTA_ADMIN_PASSWORD")
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")

	// 登入
	cookie, code := loginAndGetCookie(t, h, AdminPassword)
	if code != http.StatusOK {
		t.Fatalf("login want 200, got %d", code)
	}

	// 改密碼
	body, _ := json.Marshal(map[string]string{"current": AdminPassword, "new": "newpass123"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("change-password want 200, got %d: %s", w.Code, w.Body.String())
	}

	// status 應顯示 must_change=false
	req2 := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req2.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	var resp map[string]any
	json.NewDecoder(w2.Body).Decode(&resp)
	if mc, _ := resp["must_change"].(bool); mc {
		t.Fatal("want must_change=false after change")
	}

	// 舊密碼應失效
	_, code2 := loginAndGetCookie(t, h, AdminPassword)
	if code2 != http.StatusUnauthorized {
		t.Fatalf("old password should fail, got %d", code2)
	}

	// 新密碼可以登入
	_, code3 := loginAndGetCookie(t, h, "newpass123")
	if code3 != http.StatusOK {
		t.Fatalf("new password should work, got %d", code3)
	}
}

// TestChangePasswordWrongCurrent 確認現有密碼錯誤回 401。
func TestChangePasswordWrongCurrent(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	cookie, _ := loginAndGetCookie(t, h, AdminPassword)

	body, _ := json.Marshal(map[string]string{"current": "wrongpass", "new": "newpass123"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// TestChangePasswordWeakNew 確認新密碼太短回 400。
func TestChangePasswordWeakNew(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	cookie, _ := loginAndGetCookie(t, h, AdminPassword)

	body, _ := json.Marshal(map[string]string{"current": AdminPassword, "new": "short"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// TestLogoutClearsSession 確認登出後 session cookie 失效。
func TestLogoutClearsSession(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")

	// 先登入
	cookie, code := loginAndGetCookie(t, h, AdminPassword)
	if code != http.StatusOK {
		t.Fatalf("login want 200, got %d", code)
	}

	// 登出
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout",
		strings.NewReader("{}"))
	logoutReq.Header.Set("Content-Type", "application/json")
	logoutReq.AddCookie(cookie)
	wl := httptest.NewRecorder()
	h.ServeHTTP(wl, logoutReq)
	if wl.Code != http.StatusOK {
		t.Fatalf("logout want 200, got %d", wl.Code)
	}

	// 再用原來的 cookie 打 /api/accounts 應該 401
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("after logout want 401, got %d", w.Code)
	}
}

// TestNotificationsChannelFlow 驗證:POST 建立頻道、GET 遮罩 token、DB 存密文。
func TestNotificationsChannelFlow(t *testing.T) {
	s := testStore(t)
	cipher := testCipher(t)
	h := New(s, &oauth.Client{}, 1800, "", "", cipher, "dev")

	// POST /api/notifications/channels 建立 telegram 頻道
	body, _ := json.Marshal(map[string]any{
		"type":    "telegram",
		"enabled": true,
		"config":  map[string]string{"bot_token": "SECRET123", "chat_id": "42"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels", bytes.NewReader(body))
	req.SetBasicAuth("admin", AdminPassword)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST channels want 200, got %d: %s", w.Code, w.Body.String())
	}

	// GET /api/notifications 取回頻道列表(含門檻)
	resp := authedGet(t, h, "/api/notifications")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET notifications want 200, got %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// 序列化回字串方便做子字串檢查
	outBytes, _ := json.Marshal(out)
	outStr := string(outBytes)

	// 明文 token 不應出現
	if strings.Contains(outStr, "SECRET123") {
		t.Error("回應不應含明文 token SECRET123")
	}
	// 遮罩後的 last4 應出現
	if !strings.Contains(outStr, "T123") {
		t.Errorf("回應應含 last4 遮罩 T123，實際：%s", outStr)
	}

	// DB 存的 bot_token 應為 enc: 前綴密文
	channels, err := s.ListChannels()
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) == 0 {
		t.Fatal("DB 中應有頻道記錄")
	}
	var cfg map[string]string
	if err := json.Unmarshal([]byte(channels[0].Config), &cfg); err != nil {
		t.Fatalf("解析 config: %v", err)
	}
	if !strings.HasPrefix(cfg["bot_token"], "enc:") {
		t.Errorf("DB 中 bot_token 應以 enc: 開頭，實際：%q", cfg["bot_token"])
	}
}

// postChannel 建立一個頻道並回傳新 id。
func postChannel(t *testing.T, h http.Handler, payload map[string]any) (int64, int) {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels", bytes.NewReader(body))
	req.SetBasicAuth("admin", AdminPassword)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		return 0, w.Code
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	id, _ := resp["id"].(float64)
	return int64(id), w.Code
}

// TestNotificationsPutPreservesSecret 驗證 PUT 空 bot_token 會保留舊密文,其餘欄位照常更新。
func TestNotificationsPutPreservesSecret(t *testing.T) {
	s := testStore(t)
	cipher := testCipher(t)
	h := New(s, &oauth.Client{}, 1800, "", "", cipher, "dev")

	id, code := postChannel(t, h, map[string]any{
		"type":    "telegram",
		"enabled": true,
		"config":  map[string]string{"bot_token": "ORIGTOKEN9", "chat_id": "42"},
	})
	if code != http.StatusOK {
		t.Fatalf("POST want 200, got %d", code)
	}

	// GET 確認遮罩(不洩漏明文)
	resp := authedGet(t, h, "/api/notifications")
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	outBytes, _ := json.Marshal(out)
	if strings.Contains(string(outBytes), "ORIGTOKEN9") {
		t.Error("GET 不應含明文 token")
	}

	// PUT 空 bot_token,只改 chat_id
	putBody, _ := json.Marshal(map[string]any{
		"type":    "telegram",
		"enabled": true,
		"config":  map[string]string{"bot_token": "", "chat_id": "99"},
	})
	preq := httptest.NewRequest(http.MethodPut,
		"/api/notifications/channels/"+strconv.FormatInt(id, 10), bytes.NewReader(putBody))
	preq.SetBasicAuth("admin", AdminPassword)
	preq.Header.Set("Content-Type", "application/json")
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, preq)
	if pw.Code != http.StatusOK {
		t.Fatalf("PUT want 200, got %d: %s", pw.Code, pw.Body.String())
	}

	// DB 中 bot_token 應仍解密回原值,chat_id 更新為 99
	channels, err := s.ListChannels()
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]string
	json.Unmarshal([]byte(channels[0].Config), &cfg)
	tok, err := cipher.Decrypt(cfg["bot_token"])
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if tok != "ORIGTOKEN9" {
		t.Errorf("bot_token 應保留原值 ORIGTOKEN9,實際 %q", tok)
	}
	if cfg["chat_id"] != "99" {
		t.Errorf("chat_id 應更新為 99,實際 %q", cfg["chat_id"])
	}
}

// TestNotificationsUnknownType 驗證未知頻道型別回 400。
func TestNotificationsUnknownType(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	_, code := postChannel(t, h, map[string]any{
		"type":    "carrierpigeon",
		"enabled": true,
		"config":  map[string]string{},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", code)
	}
}

// TestUserSeriesEndpoint 驗證 GET /api/user-series 回傳正確結構。
func TestUserSeriesEndpoint(t *testing.T) {
	s := testStore(t)
	now := time.Now().Unix()
	_ = s.InsertUserCost("main", "gary", now-3600, 1.5, 500)
	_ = s.InsertUserCost("main", "gary", now-1800, 2.5, 800)

	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")
	resp := authedGet(t, h, "/api/user-series?account=main&user=gary&range=7d")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["bucket_sec"]; !ok {
		t.Error("回應缺少 bucket_sec 欄位")
	}
	if _, ok := out["points"]; !ok {
		t.Error("回應缺少 points 欄位")
	}
}

// TestDeleteUserEndpoint 驗證 DELETE /api/users 成功刪除指定使用者。
func TestDeleteUserEndpoint(t *testing.T) {
	s := testStore(t)
	now := time.Now().Unix()
	_ = s.InsertUserCost("main", "gary", now-100, 1.0, 100)

	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")

	req := httptest.NewRequest(http.MethodDelete, "/api/users?account=main&user=gary", nil)
	req.SetBasicAuth("admin", AdminPassword)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	users, err := s.DistinctUsers("main")
	if err != nil {
		t.Fatalf("DistinctUsers: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("刪除後應無使用者，剩餘: %v", users)
	}
}

// TestNotificationsThresholdsRoundTrip 驗證 PUT 門檻後 GET 取回新值。
func TestNotificationsThresholdsRoundTrip(t *testing.T) {
	s := testStore(t)
	h := New(s, &oauth.Client{}, 1800, "", "", testCipher(t), "dev")

	body, _ := json.Marshal(map[string]any{
		"SevenDayWarn": 80, "SevenDayCrit": 92, "FiveHourCrit": 97, "ResetNotify": false,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/notifications/thresholds", bytes.NewReader(body))
	req.SetBasicAuth("admin", AdminPassword)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT thresholds want 200, got %d: %s", w.Code, w.Body.String())
	}

	resp := authedGet(t, h, "/api/notifications")
	var out struct {
		Thresholds store.AlertThresholds `json:"thresholds"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Thresholds.SevenDayWarn != 80 {
		t.Errorf("SevenDayWarn want 80, got %v", out.Thresholds.SevenDayWarn)
	}
	if out.Thresholds.SevenDayCrit != 92 {
		t.Errorf("SevenDayCrit want 92, got %v", out.Thresholds.SevenDayCrit)
	}
	if out.Thresholds.FiveHourCrit != 97 {
		t.Errorf("FiveHourCrit want 97, got %v", out.Thresholds.FiveHourCrit)
	}
	if out.Thresholds.ResetNotify {
		t.Error("ResetNotify want false")
	}
}

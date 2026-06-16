package ingest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ccquota/ccquota/internal/store"
)

func getQuota(t *testing.T, h http.Handler, query, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/quota?"+query, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestQuotaHandlerReturnsReading(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	now := time.Now().Unix()
	if err := s.InsertReading(store.Reading{
		AccountID: "main", TS: now, SevenDay: 59, FiveHour: 23,
		SevenDayResetsAt: now + 3*24*3600,
	}); err != nil {
		t.Fatal(err)
	}
	h := NewQuotaHandler(s, 1800, "secret")

	rr := getQuota(t, h, "account=main", "Bearer secret")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		FiveHour   *float64 `json:"five_hour"`
		SevenDay   *float64 `json:"seven_day"`
		SharePct   *float64 `json:"share_pct"`
		Stale      bool     `json:"stale"`
		Thresholds struct {
			SevenDayCrit float64 `json:"seven_day_crit"`
		} `json:"thresholds"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.FiveHour == nil || *resp.FiveHour != 23 || resp.SevenDay == nil || *resp.SevenDay != 59 {
		t.Errorf("5h/7d 不對: %+v", resp)
	}
	if resp.SharePct != nil {
		t.Errorf("沒帶 user,share_pct 應為 null,got %v", *resp.SharePct)
	}
	if resp.Thresholds.SevenDayCrit != 90 {
		t.Errorf("thresholds 預設應為 90,got %v", resp.Thresholds.SevenDayCrit)
	}
}

func TestQuotaHandlerSharePct(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	now := time.Now().Unix()
	resetsAt := now + 3*24*3600
	if err := s.InsertReading(store.Reading{
		AccountID: "main", TS: now, SevenDay: 50, FiveHour: 10, SevenDayResetsAt: resetsAt,
	}); err != nil {
		t.Fatal(err)
	}
	sinceTS := resetsAt - 7*24*3600
	if err := s.InsertUserCost("main", "alice", sinceTS+10, 30, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertUserCost("main", "bob", sinceTS+10, 10, 500); err != nil {
		t.Fatal(err)
	}
	h := NewQuotaHandler(s, 1800, "secret")

	// alice 花得多(30 > 10),share_pct 應較高;不存在的 user 回 null。
	aliceShare := quotaShare(t, h, "alice")
	if aliceShare == nil || *aliceShare <= 0 {
		t.Fatalf("alice 的 share_pct 應 >0, got %v", aliceShare)
	}
	bobShare := quotaShare(t, h, "bob")
	if bobShare == nil || *bobShare <= 0 {
		t.Fatalf("bob 的 share_pct 應 >0, got %v", bobShare)
	}
	if *aliceShare <= *bobShare {
		t.Errorf("alice 花得多,share 應 > bob: alice=%v bob=%v", *aliceShare, *bobShare)
	}
	if ghostShare := quotaShare(t, h, "ghost"); ghostShare != nil {
		t.Errorf("不存在的 user,share_pct 應為 null, got %v", *ghostShare)
	}
}

// quotaShare 查 main 帳號某 user 的 share_pct(nil = JSON 為 null)。
func quotaShare(t *testing.T, h http.Handler, user string) *float64 {
	t.Helper()
	rr := getQuota(t, h, "account=main&user="+user, "Bearer secret")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp struct {
		SharePct *float64 `json:"share_pct"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.SharePct
}

func TestQuotaHandlerStale(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	now := time.Now().Unix()
	resetsAt := now + 3*24*3600
	// stale=1800;old 的 reading TS 比 now 早 2000 秒,超過門檻 → stale。
	if err := s.InsertReading(store.Reading{
		AccountID: "old", TS: now - 2000, SevenDay: 50, FiveHour: 10, SevenDayResetsAt: resetsAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertReading(store.Reading{
		AccountID: "fresh", TS: now, SevenDay: 50, FiveHour: 10, SevenDayResetsAt: resetsAt,
	}); err != nil {
		t.Fatal(err)
	}
	h := NewQuotaHandler(s, 1800, "secret")

	if !quotaStale(t, h, "old") {
		t.Error("舊 reading(TS 早 2000 秒)應為 stale")
	}
	if quotaStale(t, h, "fresh") {
		t.Error("新 reading(TS = now)不應為 stale")
	}
}

// quotaStale 查某帳號的 stale 旗標。
func quotaStale(t *testing.T, h http.Handler, account string) bool {
	t.Helper()
	rr := getQuota(t, h, "account="+account, "Bearer secret")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp struct {
		Stale bool `json:"stale"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Stale
}

func TestQuotaHandlerNoReading(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewQuotaHandler(s, 1800, "secret")

	rr := getQuota(t, h, "account=ghost", "Bearer secret")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp struct {
		FiveHour *float64 `json:"five_hour"`
		SevenDay *float64 `json:"seven_day"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.FiveHour != nil || resp.SevenDay != nil {
		t.Errorf("無 reading 時 5h/7d 應為 null: %+v", resp)
	}
}

func TestQuotaHandlerAuth(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewQuotaHandler(s, 1800, "secret")

	if rr := getQuota(t, h, "account=main", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("無 token 應 401, got %d", rr.Code)
	}
	if rr := getQuota(t, h, "account=main", "Bearer wrong"); rr.Code != http.StatusUnauthorized {
		t.Errorf("錯 token 應 401, got %d", rr.Code)
	}
	if rr := getQuota(t, h, "", "Bearer secret"); rr.Code != http.StatusBadRequest {
		t.Errorf("缺 account 應 400, got %d", rr.Code)
	}
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/quota?account=main", nil)
		req.Header.Set("Authorization", "Bearer secret")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("non-GET 應 405, got %d", rr.Code)
		}
	}
}

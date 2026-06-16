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

	rr := getQuota(t, h, "account=main&user=alice", "Bearer secret")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp struct {
		SharePct *float64 `json:"share_pct"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.SharePct == nil || *resp.SharePct <= 0 {
		t.Errorf("alice 的 share_pct 應 >0, got %v", resp.SharePct)
	}
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
	json.Unmarshal(rr.Body.Bytes(), &resp)
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
}

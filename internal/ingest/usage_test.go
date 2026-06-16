package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ccquota/ccquota/internal/store"
)

const usageBody = `{"five_hour":{"utilization":8,"resets_at":""},"seven_day":{"utilization":47.4,"resets_at":""}}`

func postUsage(t *testing.T, h http.Handler, account, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/usage?account="+account, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestUsageHandlerInserts(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewUsageHandler(s, "secret", nil)

	rr := postUsage(t, h, "main", "Bearer secret", usageBody)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rr.Code, rr.Body.String())
	}
	r, ok, _ := s.LatestReading("main")
	if !ok {
		t.Fatal("沒寫入 reading")
	}
	if r.SevenDay != 47.4 || r.FiveHour != 8 {
		t.Errorf("reading 數值不對: 7d=%v 5h=%v", r.SevenDay, r.FiveHour)
	}
}

func TestUsageHandlerAuth(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewUsageHandler(s, "secret", nil)

	if rr := postUsage(t, h, "main", "Bearer wrong", usageBody); rr.Code != http.StatusUnauthorized {
		t.Errorf("錯 token 應 401，得 %d", rr.Code)
	}
	if rr := postUsage(t, h, "main", "", usageBody); rr.Code != http.StatusUnauthorized {
		t.Errorf("無 token 應 401，得 %d", rr.Code)
	}
}

func TestUsageHandlerBadRequest(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewUsageHandler(s, "secret", nil)

	if rr := postUsage(t, h, "", "Bearer secret", usageBody); rr.Code != http.StatusBadRequest {
		t.Errorf("缺 account 應 400，得 %d", rr.Code)
	}
	if rr := postUsage(t, h, "main", "Bearer secret", "not json"); rr.Code != http.StatusBadRequest {
		t.Errorf("壞 JSON 應 400，得 %d", rr.Code)
	}
}

// TestUsageHandlerResetFires 確認推送端也會偵測額度重置並呼叫 onReset。
func TestUsageHandlerResetFires(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	var fired bool
	h := NewUsageHandler(s, "secret", func(_ string, _, _ float64) { fired = true })

	// 第一筆建立 baseline（resets_at 非零），第二筆用量驟降 -> reset。
	postUsage(t, h, "main", "Bearer secret", `{"seven_day":{"utilization":80,"resets_at":"2026-06-20T00:00:00Z"}}`)
	postUsage(t, h, "main", "Bearer secret", `{"seven_day":{"utilization":2,"resets_at":"2026-06-20T00:00:00Z"}}`)
	if !fired {
		t.Error("用量驟降應觸發 reset onReset")
	}
}

package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ccquota/ccquota/internal/store"
)

func TestBackfillHandler(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	h := NewBackfillHandler(s, "secret")

	body := `{"account":"main","user":"gary","tokens":5000,"window_start":500,"cutoff":1000}`
	req := httptest.NewRequest(http.MethodPost, "/v1/backfill", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	uc, _ := s.UserPeriodCosts("main", 0)
	if uc["gary"].Tokens != 5000 {
		t.Fatalf("回填後 gary tokens 應 5000,得 %d", uc["gary"].Tokens)
	}

	// 無認證 → 401
	req2 := httptest.NewRequest(http.MethodPost, "/v1/backfill", strings.NewReader(body))
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("無認證應 401,得 %d", rr2.Code)
	}
}

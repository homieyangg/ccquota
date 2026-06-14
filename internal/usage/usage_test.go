package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TK" {
			t.Errorf("missing bearer: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Errorf("missing beta header")
		}
		w.Write([]byte(`{
		  "five_hour":{"utilization":8.0,"resets_at":"2026-06-14T19:19:59.5+00:00"},
		  "seven_day":{"utilization":14.0,"resets_at":"2026-06-19T03:59:59.5+00:00"},
		  "seven_day_sonnet":{"utilization":2.0,"resets_at":"2026-06-19T03:59:59.5+00:00"},
		  "seven_day_opus":null
		}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), URL: srv.URL}
	s, err := c.Fetch(context.Background(), "TK")
	if err != nil {
		t.Fatal(err)
	}
	if s.SevenDay != 14 || s.FiveHour != 8 || s.Sonnet != 2 || s.Opus != 0 {
		t.Fatalf("bad snapshot: %+v", s)
	}
	if s.SevenDayResetsAt != 1781841599 { // 2026-06-19T03:59:59Z
		t.Fatalf("bad reset epoch: %d", s.SevenDayResetsAt)
	}
}

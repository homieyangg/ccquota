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

// TestParseLimitsArray:新版回應把額度搬進 limits[],頂層 sonnet/opus 已是 null。
// weekly_scoped 應解析成 ScopedPct 並帶模型名。
func TestParseLimitsArray(t *testing.T) {
	body := []byte(`{
	  "five_hour":{"utilization":26.0,"resets_at":"2026-09-03T10:59:59+00:00"},
	  "seven_day":{"utilization":42.0,"resets_at":"2026-09-08T15:59:59+00:00"},
	  "seven_day_sonnet":null,
	  "seven_day_opus":null,
	  "limits":[
	    {"kind":"session","percent":26,"resets_at":"2026-09-03T10:59:59+00:00","scope":null,"is_active":false},
	    {"kind":"weekly_all","percent":42,"resets_at":"2026-09-08T15:59:59+00:00","scope":null,"is_active":false},
	    {"kind":"weekly_scoped","percent":71,"resets_at":"2026-09-08T15:59:59+00:00",
	     "scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":true}
	  ]
	}`)
	s, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if s.SevenDay != 42 || s.FiveHour != 26 {
		t.Errorf("頂層額度不對: %+v", s)
	}
	if s.ScopedPct != 71 || s.ScopedLabel != "Fable" {
		t.Errorf("weekly_scoped 應為 71%% Fable,得 %v %q", s.ScopedPct, s.ScopedLabel)
	}
	if s.ScopedResetsAt != 1788883199 { // 2026-09-08T15:59:59Z
		t.Errorf("scoped reset epoch 不對: %d", s.ScopedResetsAt)
	}
	if s.Sonnet != 0 || s.Opus != 0 {
		t.Errorf("sonnet/opus 已停用應為 0: %+v", s)
	}
}

// TestParseLimitsBeatsTopLevel:limits[] 的百分比高於頂層欄位時要以 limits[] 為準。
func TestParseLimitsBeatsTopLevel(t *testing.T) {
	s, err := Parse([]byte(`{
	  "seven_day":{"utilization":10.0,"resets_at":"2026-09-08T15:59:59+00:00"},
	  "limits":[{"kind":"weekly_all","percent":55,"resets_at":"2026-09-09T15:59:59+00:00","is_active":true}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.SevenDay != 55 {
		t.Errorf("應取 limits[] 的 55,得 %v", s.SevenDay)
	}
	if s.SevenDayResetsAt != 1788969599 { // 2026-09-09T15:59:59Z
		t.Errorf("reset 應跟著 limits[] 更新,得 %d", s.SevenDayResetsAt)
	}
}

// TestParseNoLimitsArray:舊版回應沒有 limits[] 時維持原行為。
func TestParseNoLimitsArray(t *testing.T) {
	s, err := Parse([]byte(`{
	  "five_hour":{"utilization":8.0,"resets_at":"2026-06-14T19:19:59+00:00"},
	  "seven_day":{"utilization":14.0,"resets_at":"2026-06-19T03:59:59+00:00"},
	  "seven_day_sonnet":{"utilization":2.0}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.SevenDay != 14 || s.FiveHour != 8 || s.Sonnet != 2 {
		t.Errorf("舊格式應維持原行為: %+v", s)
	}
	if s.ScopedPct != 0 || s.ScopedLabel != "" {
		t.Errorf("沒有 limits[] 不該有 scoped: %+v", s)
	}
}

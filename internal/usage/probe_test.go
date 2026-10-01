package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setLimitHeaders(w http.ResponseWriter, kv map[string]string) {
	for k, v := range kv {
		w.Header().Set(headerPrefix+k, v)
	}
}

// TestProbeFetch:發最小推論請求,從 header 讀出 5h / 7d / 模型別週限。
func TestProbeFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer LONG" {
			t.Errorf("missing bearer: %q", r.Header.Get("Authorization"))
		}
		var req struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if req.Model != ProbeModel || req.MaxTokens != 1 {
			t.Errorf("應打預設模型且 max_tokens=1,得 %+v", req)
		}
		setLimitHeaders(w, map[string]string{
			"5h-utilization": "0.24", "5h-reset": "1790839200",
			"7d-utilization": "0.5", "7d-reset": "1791302400",
			"7d_oi-utilization": "0.713", "7d_oi-reset": "1791302400",
		})
		w.Write([]byte(`{"content":[]}`))
	}))
	defer srv.Close()

	s, err := (&ProbeClient{HTTP: srv.Client(), URL: srv.URL}).Fetch(context.Background(), "LONG")
	if err != nil {
		t.Fatal(err)
	}
	if s.FiveHour != 24 || s.SevenDay != 50 || s.ScopedPct != 71.3 {
		t.Errorf("比例應轉成百分比: %+v", s)
	}
	if s.FiveHourResetsAt != 1790839200 || s.SevenDayResetsAt != 1791302400 || s.ScopedResetsAt != 1791302400 {
		t.Errorf("reset 時間不對: %+v", s)
	}
}

// TestProbeFetchRateLimited:額度用完時請求回 429,但 header 還在,要照樣採用。
func TestProbeFetchRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setLimitHeaders(w, map[string]string{"5h-utilization": "1.0", "7d-utilization": "0.62"})
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s, err := (&ProbeClient{HTTP: srv.Client(), URL: srv.URL}).Fetch(context.Background(), "LONG")
	if err != nil {
		t.Fatal(err)
	}
	if s.FiveHour != 100 || s.SevenDay != 62 {
		t.Errorf("429 也要讀 header: %+v", s)
	}
}

// TestProbeFetchNoHeaders:沒有額度 header(例如 401)就回錯誤,不寫入假的 0%。
func TestProbeFetchNoHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad token"}`))
	}))
	defer srv.Close()

	if _, err := (&ProbeClient{HTTP: srv.Client(), URL: srv.URL}).Fetch(context.Background(), "BAD"); err == nil {
		t.Fatal("沒有 header 應回錯誤")
	}
}

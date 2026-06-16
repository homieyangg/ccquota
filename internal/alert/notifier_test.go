package alert_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ccquota/ccquota/internal/alert"
	"github.com/ccquota/ccquota/internal/store"
)

// captureSink 收集所有 Send 的訊息，供斷言用。
type captureSink struct {
	msgs []string
	lang string
}

func (c *captureSink) Send(_ context.Context, text string) error {
	c.msgs = append(c.msgs, text)
	return nil
}

func (c *captureSink) Lang() string { return c.lang }

func openMemStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- i18n template tests ---

func TestTemplateRendering(t *testing.T) {
	tests := []struct {
		lang    string
		key     string
		args    []any
		wantSub string // 必須出現在輸出中的子字串
	}{
		// en
		{"en", "reset", []any{"acct1", 80.0, 5.0}, "Quota Reset"},
		{"en", "reset", []any{"acct1", 80.0, 5.0}, "80%"},
		{"en", "weekly_warn", []any{"acct1", 76.0}, "76%"},
		{"en", "weekly_crit", []any{"acct1", 91.0}, "91%"},
		{"en", "five_hour_crit", []any{"acct1", 96.0}, "96%"},
		{"en", "stale", []any{"acct1", int64(3600)}, "3600"},
		// zh-TW
		{"zh-TW", "reset", []any{"acct1", 80.0, 5.0}, "重置"},
		{"zh-TW", "weekly_warn", []any{"acct1", 76.0}, "週配額"},
		{"zh-TW", "five_hour_crit", []any{"acct1", 96.0}, "5小時"},
		// zh-CN
		{"zh-CN", "reset", []any{"acct1", 80.0, 5.0}, "重置"},
		{"zh-CN", "weekly_warn", []any{"acct1", 76.0}, "周配额"},
		// fallback: unknown lang → en
		{"ja", "reset", []any{"acct1", 80.0, 5.0}, "Quota Reset"},
		// fallback: unknown key → "" (en key missing → empty-ish; just no panic)
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s/%s", tc.lang, tc.key), func(t *testing.T) {
			got := alert.RenderTemplate(tc.lang, tc.key, tc.args...)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("RenderTemplate(%q,%q) = %q; want substring %q", tc.lang, tc.key, got, tc.wantSub)
			}
		})
	}
}

// --- Reset ---

func TestNotifierReset(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{}
	n := alert.NewNotifier(alert.Config{Lang: "en"}, s, sink)

	if err := n.Reset(context.Background(), "acct1", 80, 5); err != nil {
		t.Fatal(err)
	}
	if len(sink.msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(sink.msgs))
	}
	if !strings.Contains(sink.msgs[0], "Reset") {
		t.Errorf("msg should mention Reset: %q", sink.msgs[0])
	}
	// Reset 不 dedup，再呼叫一次也要送
	_ = n.Reset(context.Background(), "acct1", 80, 5)
	if len(sink.msgs) != 2 {
		t.Fatalf("want 2 msgs after second reset, got %d", len(sink.msgs))
	}
}

// --- Thresholds dedup ---

func TestThresholdsDedup(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{}
	n := alert.NewNotifier(alert.Config{
		Lang:         "en",
		WeeklyWarn:   75,
		WeeklyCrit:   90,
		FiveHourCrit: 95,
	}, s, sink)

	// 7d=76% (>warn, <crit), 5h=50% (ok), 應發 warn
	if err := n.Thresholds(context.Background(), "acct1", 76, 50, 1000, 2000); err != nil {
		t.Fatal(err)
	}
	if len(sink.msgs) != 1 {
		t.Fatalf("first call: want 1 msg, got %d: %v", len(sink.msgs), sink.msgs)
	}
	if !strings.Contains(sink.msgs[0], "Warning") {
		t.Errorf("expected Warning, got: %q", sink.msgs[0])
	}

	// 同一視窗再呼叫 → dedup，不重送
	_ = n.Thresholds(context.Background(), "acct1", 78, 50, 1000, 2000)
	if len(sink.msgs) != 1 {
		t.Fatalf("dedup: want still 1 msg, got %d", len(sink.msgs))
	}

	// 7d=91% (>crit), 同視窗 → crit 尚未 fired，應發 crit（warn 已 fired 不再發）
	_ = n.Thresholds(context.Background(), "acct1", 91, 50, 1000, 2000)
	if len(sink.msgs) != 2 {
		t.Fatalf("crit: want 2 msgs, got %d: %v", len(sink.msgs), sink.msgs)
	}
	if !strings.Contains(sink.msgs[1], "Critical") {
		t.Errorf("expected Critical, got: %q", sink.msgs[1])
	}

	// 新視窗 (sevenDayResetsAt=9999) → warn re-arm
	_ = n.Thresholds(context.Background(), "acct1", 76, 50, 9999, 2000)
	if len(sink.msgs) != 3 {
		t.Fatalf("re-arm: want 3 msgs, got %d: %v", len(sink.msgs), sink.msgs)
	}

	// 5h crit
	_ = n.Thresholds(context.Background(), "acct1", 10, 96, 9999, 2000)
	if len(sink.msgs) != 4 {
		t.Fatalf("5h crit: want 4 msgs, got %d: %v", len(sink.msgs), sink.msgs)
	}
	if !strings.Contains(sink.msgs[3], "5-Hour") {
		t.Errorf("expected 5-Hour, got: %q", sink.msgs[3])
	}

	// 5h 同視窗再觸發 → dedup
	_ = n.Thresholds(context.Background(), "acct1", 10, 97, 9999, 2000)
	if len(sink.msgs) != 4 {
		t.Fatalf("5h dedup: want still 4 msgs, got %d", len(sink.msgs))
	}
}

// --- No sinks configured ---

func TestNotifierNoSinks(t *testing.T) {
	s := openMemStore(t)
	n := alert.NewNotifier(alert.Config{Lang: "en", WeeklyWarn: 75, WeeklyCrit: 90, FiveHourCrit: 95}, s)

	// 不應 panic 或 error
	if err := n.Reset(context.Background(), "acct1", 80, 5); err != nil {
		t.Fatal(err)
	}
	if err := n.Thresholds(context.Background(), "acct1", 80, 80, 1000, 2000); err != nil {
		t.Fatal(err)
	}
	if err := n.Stale(context.Background(), "acct1", 1900); err != nil {
		t.Fatal(err)
	}
}

// --- Stale throttle ---

func TestStaleThrottle(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{}
	n := alert.NewNotifier(alert.Config{
		Lang:           "en",
		PollerStaleSec: 1800,
	}, s, sink)

	// ageSec < threshold → no alert
	_ = n.Stale(context.Background(), "acct1", 1799)
	if len(sink.msgs) != 0 {
		t.Fatalf("below threshold: want 0 msgs, got %d", len(sink.msgs))
	}

	// ageSec >= threshold → alert
	_ = n.Stale(context.Background(), "acct1", 1800)
	if len(sink.msgs) != 1 {
		t.Fatalf("at threshold: want 1 msg, got %d", len(sink.msgs))
	}

	// 同 6h bucket → dedup
	_ = n.Stale(context.Background(), "acct1", 5000)
	if len(sink.msgs) != 1 {
		t.Fatalf("same bucket: want still 1 msg, got %d", len(sink.msgs))
	}

	// 不同 6h bucket (21600+1800 = 23400)
	_ = n.Stale(context.Background(), "acct1", 23400)
	if len(sink.msgs) != 2 {
		t.Fatalf("new bucket: want 2 msgs, got %d", len(sink.msgs))
	}
}

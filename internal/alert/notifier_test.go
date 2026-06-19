package alert_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ccquota/ccquota/internal/alert"
	"github.com/ccquota/ccquota/internal/store"
)

// captureSink 收集所有 Send / Edit 的訊息,供斷言用。
// Send 回傳遞增的 ref(模擬 Telegram message_id),Edit 記在 edits。
type captureSink struct {
	msgs   []string
	edits  []string
	lang   string
	key    string
	refN   int
	noEdit bool // true 模擬不支援 edit 的 sink(如 webhook)
}

func (c *captureSink) Send(_ context.Context, text string) (string, error) {
	c.msgs = append(c.msgs, text)
	c.refN++
	return fmt.Sprintf("ref%d", c.refN), nil
}

func (c *captureSink) Edit(_ context.Context, ref, text string) error {
	if c.noEdit {
		return alert.ErrEditUnsupported
	}
	c.edits = append(c.edits, ref+"|"+text)
	return nil
}

func (c *captureSink) Lang() string { return c.lang }

func (c *captureSink) Key() string {
	if c.key != "" {
		return c.key
	}
	return "cap"
}

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

// TestWeeklyTemplateWithBudget:weekly 文案帶上反推額度與本週剩餘。
func TestWeeklyTemplateWithBudget(t *testing.T) {
	suffix := alert.RenderTemplate("zh-TW", "weekly_budget_suffix", 892.0, 214.0)
	msg := alert.RenderTemplate("zh-TW", "weekly_warn", "main", 76.0, suffix)
	for _, want := range []string{"76%", "892", "214"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("文案缺 %q: %q", want, msg)
		}
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

	// 76% → warn,送一則
	if err := n.Thresholds(context.Background(), "acct1", 76, 50, 0, 0, 1000, 2000); err != nil {
		t.Fatal(err)
	}
	if len(sink.msgs) != 1 || !strings.Contains(sink.msgs[0], "Warning") {
		t.Fatalf("warn: msgs=%v", sink.msgs)
	}

	// 78% 同視窗 → 已 warn,不動
	_ = n.Thresholds(context.Background(), "acct1", 78, 50, 0, 0, 1000, 2000)
	if len(sink.msgs) != 1 {
		t.Fatalf("warn dedup: want 1 msg, got %d", len(sink.msgs))
	}

	// 91% 同視窗 → 升 crit:就地編輯,不新發
	_ = n.Thresholds(context.Background(), "acct1", 91, 50, 0, 0, 1000, 2000)
	if len(sink.msgs) != 1 {
		t.Fatalf("escalate 不該新發,msgs=%d", len(sink.msgs))
	}
	if len(sink.edits) != 1 || !strings.Contains(sink.edits[0], "Critical") {
		t.Fatalf("escalate 應 edit 成 crit,edits=%v", sink.edits)
	}

	// 已 crit 同視窗 → 不動
	_ = n.Thresholds(context.Background(), "acct1", 95, 50, 0, 0, 1000, 2000)
	if len(sink.edits) != 1 {
		t.Fatalf("已 crit 不該再 edit,edits=%d", len(sink.edits))
	}

	// 新視窗 → warn re-arm,新發一則
	_ = n.Thresholds(context.Background(), "acct1", 76, 50, 0, 0, 9999, 2000)
	if len(sink.msgs) != 2 {
		t.Fatalf("新視窗 re-arm,msgs=%d", len(sink.msgs))
	}

	// 5h crit
	_ = n.Thresholds(context.Background(), "acct1", 10, 96, 0, 0, 9999, 2000)
	if len(sink.msgs) != 3 || !strings.Contains(sink.msgs[2], "5-Hour") {
		t.Fatalf("5h crit,msgs=%v", sink.msgs)
	}

	// 5h 同視窗再觸發 → dedup
	_ = n.Thresholds(context.Background(), "acct1", 10, 97, 0, 0, 9999, 2000)
	if len(sink.msgs) != 3 {
		t.Fatalf("5h dedup,msgs=%d", len(sink.msgs))
	}
}

// TestWeeklyEscalateWebhookDegrade:不支援 edit 的 sink(webhook)升級時退化成重送一則 crit。
func TestWeeklyEscalateWebhookDegrade(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{noEdit: true}
	n := alert.NewNotifier(alert.Config{Lang: "en", WeeklyWarn: 75, WeeklyCrit: 90, FiveHourCrit: 95}, s, sink)

	_ = n.Thresholds(context.Background(), "acct1", 76, 0, 0, 0, 1000, 2000) // warn → send
	_ = n.Thresholds(context.Background(), "acct1", 91, 0, 0, 0, 1000, 2000) // crit,edit 不支援 → 退化重送
	if len(sink.msgs) != 2 {
		t.Fatalf("不支援 edit 應退化重送,msgs=%d", len(sink.msgs))
	}
	if !strings.Contains(sink.msgs[1], "Critical") {
		t.Errorf("退化重送應為 crit,得 %q", sink.msgs[1])
	}
}

// TestThresholdsDedupResetsAtJitter:resets_at 因 sub-second timestamp 轉 epoch 會 ±1s 抖動,
// 同一個視窗的 warn 只該發一次,不能因為錨點抖了 1 秒就重送。
func TestThresholdsDedupResetsAtJitter(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{}
	n := alert.NewNotifier(alert.Config{Lang: "en", WeeklyWarn: 75, WeeklyCrit: 90, FiveHourCrit: 95}, s, sink)

	_ = n.Thresholds(context.Background(), "acct1", 76, 50, 0, 0, 1781841600, 2000)
	_ = n.Thresholds(context.Background(), "acct1", 77, 50, 0, 0, 1781841599, 2000) // 抖 -1s
	_ = n.Thresholds(context.Background(), "acct1", 78, 50, 0, 0, 1781841600, 2000) // 抖回來
	if len(sink.msgs) != 1 {
		t.Fatalf("resets_at ±1s 抖動應只發一次 warn,得 %d: %v", len(sink.msgs), sink.msgs)
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
	if err := n.Thresholds(context.Background(), "acct1", 80, 80, 0, 0, 1000, 2000); err != nil {
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

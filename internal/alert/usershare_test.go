package alert_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ccquota/ccquota/internal/alert"
)

// failSink 永遠送失敗,且記錄送了幾次,用來驗 mark-on-error 契約。
type failSink struct{ sends int }

func (f *failSink) Send(_ context.Context, _ string) (string, error) {
	f.sends++
	return "", context.Canceled
}
func (f *failSink) Edit(_ context.Context, _, _ string) error { return alert.ErrEditUnsupported }
func (f *failSink) Lang() string                              { return "" }
func (f *failSink) Key() string                               { return "fail" }

func usEnabledCfg() alert.Config {
	return alert.Config{UserShareNotify: true, UserShareWarn: 150, UserShareCrit: 250}
}

func TestUserShareDisabledByDefault(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{}
	n := alert.NewNotifier(alert.Config{}, s, sink) // UserShareNotify 預設 false
	_ = n.UserShareThresholds(context.Background(), "main",
		[]alert.UserShareReading{{User: "leo", SharePct: 999, Cost: 100}}, 10, 1000)
	if len(sink.msgs) != 0 {
		t.Fatalf("預設關閉不該發,卻發了 %d 則", len(sink.msgs))
	}
}

func TestUserShareCritWinsSingleFire(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{}
	n := alert.NewNotifier(usEnabledCfg(), s, sink)
	// 260% >= crit(250) → 只發一則 crit,不該 warn+crit 雙發。
	_ = n.UserShareThresholds(context.Background(), "main",
		[]alert.UserShareReading{{User: "leo", SharePct: 260, Cost: 200}}, 80, 1000)
	if len(sink.msgs) != 1 {
		t.Fatalf("應只發 1 則(crit),卻 %d 則: %v", len(sink.msgs), sink.msgs)
	}
	if !strings.Contains(sink.msgs[0], "high") {
		t.Errorf("應為 crit 訊息,得: %s", sink.msgs[0])
	}
}

func TestUserShareResetsZeroSkipped(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{}
	n := alert.NewNotifier(usEnabledCfg(), s, sink)
	_ = n.UserShareThresholds(context.Background(), "main",
		[]alert.UserShareReading{{User: "leo", SharePct: 999, Cost: 100}}, 10, 0) // resets_at=0
	if len(sink.msgs) != 0 {
		t.Fatalf("resets_at=0 應跳過,卻發了 %d 則", len(sink.msgs))
	}
}

func TestUserShareDedupAndReArm(t *testing.T) {
	s := openMemStore(t)
	sink := &captureSink{}
	n := alert.NewNotifier(usEnabledCfg(), s, sink)
	r := []alert.UserShareReading{{User: "leo", SharePct: 160, Cost: 150}}
	_ = n.UserShareThresholds(context.Background(), "main", r, 90, 1000)
	_ = n.UserShareThresholds(context.Background(), "main", r, 90, 1000) // 同視窗 → dedup
	if len(sink.msgs) != 1 {
		t.Fatalf("同視窗應只發 1 則,卻 %d", len(sink.msgs))
	}
	_ = n.UserShareThresholds(context.Background(), "main", r, 90, 2000) // 新視窗 → re-arm
	if len(sink.msgs) != 2 {
		t.Fatalf("換視窗應 re-arm 再發,共應 2 則,卻 %d", len(sink.msgs))
	}
}

func TestUserSharePerChannelLang(t *testing.T) {
	s := openMemStore(t)
	en := &captureSink{lang: "en"}
	tw := &captureSink{lang: "zh-TW"}
	n := alert.NewNotifier(usEnabledCfg(), s, en, tw)
	_ = n.UserShareThresholds(context.Background(), "main",
		[]alert.UserShareReading{{User: "leo", SharePct: 300, Cost: 200}}, 80, 1000)
	if len(en.msgs) != 1 || !strings.Contains(en.msgs[0], "Fair-share") {
		t.Errorf("en sink 應收英文: %v", en.msgs)
	}
	if len(tw.msgs) != 1 || !strings.Contains(tw.msgs[0], "平分額度") {
		t.Errorf("zh-TW sink 應收繁中: %v", tw.msgs)
	}
}

// TestMarkOnErrorContract:任一 sink 送失敗就不 mark,下輪重送(維持原契約)。
func TestMarkOnErrorContract(t *testing.T) {
	s := openMemStore(t)
	fail := &failSink{}
	n := alert.NewNotifier(usEnabledCfg(), s, fail)
	r := []alert.UserShareReading{{User: "leo", SharePct: 300, Cost: 200}}
	_ = n.UserShareThresholds(context.Background(), "main", r, 80, 1000)
	_ = n.UserShareThresholds(context.Background(), "main", r, 80, 1000)
	if fail.sends != 2 {
		t.Fatalf("送失敗不該 mark,應重送(共 2 次),卻 %d 次", fail.sends)
	}
}

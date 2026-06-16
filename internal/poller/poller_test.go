package poller

import (
	"context"
	"errors"
	"testing"

	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
)

type fakeUsage struct{ snap usage.Snapshot }

func (f fakeUsage) Fetch(_ context.Context, _ string) (usage.Snapshot, error) { return f.snap, nil }

type fakeRefresher struct {
	calls int
	err   error
}

func (f *fakeRefresher) Refresh(_ context.Context, _ string) (oauth.Token, error) {
	f.calls++
	return oauth.Token{}, f.err
}

type countingUsage struct{ calls int }

func (c *countingUsage) Fetch(_ context.Context, _ string) (usage.Snapshot, error) {
	c.calls++
	return usage.Snapshot{}, nil
}

// TestExternalAccountSkipped 確保「外部餵入」帳號（無 refresh token）不會 refresh、
// 不會 fetch usage、也不寫 reading，避免再戳被限流的 token endpoint。
func TestExternalAccountSkipped(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	fr := &fakeRefresher{}
	cu := &countingUsage{}
	p := &Poller{Store: s, Usage: cu, OAuth: fr, Now: func() int64 { return 1000 }}

	acct := store.Account{ID: "ext", AccessToken: "", RefreshToken: "", ExpiresAt: 0}
	if err := p.cycle(context.Background(), acct); err != nil {
		t.Fatalf("cycle 不該回錯: %v", err)
	}
	if fr.calls != 0 {
		t.Errorf("外部帳號不該 refresh，卻呼叫 %d 次", fr.calls)
	}
	if cu.calls != 0 {
		t.Errorf("外部帳號不該 fetch usage，卻呼叫 %d 次", cu.calls)
	}
	if _, ok, _ := s.LatestReading("ext"); ok {
		t.Error("外部帳號 cycle 不該寫入 reading")
	}
}

func TestRefreshBackoff(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	_ = s.UpsertAccount(store.Account{ID: "a", Label: "a", AccessToken: "AT", RefreshToken: "RT", ExpiresAt: 0})

	now := int64(1000)
	fr := &fakeRefresher{err: errors.New("429 rate limited")}
	p := &Poller{
		Store:      s,
		Usage:      fakeUsage{snap: usage.Snapshot{SevenDay: 10}},
		OAuth:      fr,
		MinBackoff: 600,
		MaxBackoff: 3600,
		Now:        func() int64 { return now },
	}
	acct := store.Account{ID: "a", AccessToken: "AT", RefreshToken: "RT", ExpiresAt: 0} // expired -> wants refresh

	// 第一輪:嘗試 refresh(失敗),設下退避窗口
	_ = p.cycle(context.Background(), acct)
	if fr.calls != 1 {
		t.Fatalf("first cycle: want 1 refresh attempt, got %d", fr.calls)
	}
	// 退避窗口內再跑幾輪:不應再打 refresh
	_ = p.cycle(context.Background(), acct)
	_ = p.cycle(context.Background(), acct)
	if fr.calls != 1 {
		t.Fatalf("within backoff: want still 1 attempt, got %d", fr.calls)
	}
	// 時間跨過退避窗口:應再嘗試一次
	now += 700
	_ = p.cycle(context.Background(), acct)
	if fr.calls != 2 {
		t.Fatalf("after backoff window: want 2 attempts, got %d", fr.calls)
	}
}

func TestCycleStoresReadingAndDetectsReset(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	_ = s.UpsertAccount(store.Account{ID: "a", Label: "a", AccessToken: "AT", RefreshToken: "RT", ExpiresAt: 1 << 40})

	// seed a prior reading at 18% so a drop to 6% is a reset
	_ = s.InsertReading(store.Reading{AccountID: "a", TS: 1, SevenDay: 18, SevenDayResetsAt: 100})

	var resetMsgs []string
	p := &Poller{
		Store:   s,
		Usage:   fakeUsage{snap: usage.Snapshot{SevenDay: 6, FiveHour: 3, SevenDayResetsAt: 100}},
		DropPct: 5,
		Now:     func() int64 { return 1000 },
		OnReset: func(acct string, from, to float64) { resetMsgs = append(resetMsgs, acct) },
	}
	if err := p.cycle(context.Background(), store.Account{ID: "a", AccessToken: "AT", RefreshToken: "RT", ExpiresAt: 1 << 40}); err != nil {
		t.Fatal(err)
	}

	last, ok, _ := s.LatestReading("a")
	if !ok || last.SevenDay != 6 {
		t.Fatalf("reading not stored: %+v ok=%v", last, ok)
	}
	if len(resetMsgs) != 1 {
		t.Fatalf("expected 1 reset callback, got %d", len(resetMsgs))
	}
}

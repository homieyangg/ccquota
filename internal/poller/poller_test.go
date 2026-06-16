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

// TestClientFedAccountPollsUsageNoRefresh:client 餵 token 的帳號(有 access、
// 無 refresh token)應該拉 usage 寫 reading,但絕不 refresh(不碰 token endpoint)。
func TestClientFedAccountPollsUsageNoRefresh(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	fr := &fakeRefresher{}
	cu := &countingUsage{}
	p := &Poller{Store: s, Usage: cu, OAuth: fr, Now: func() int64 { return 1000 }}

	// 有 access token、無 refresh token、已過期(若會 refresh 就會觸發)。
	acct := store.Account{ID: "fed", AccessToken: "AT", RefreshToken: "", ExpiresAt: 0}
	if err := p.cycle(context.Background(), acct); err != nil {
		t.Fatalf("cycle 不該回錯: %v", err)
	}
	if fr.calls != 0 {
		t.Errorf("無 refresh token 不該 refresh，卻呼叫 %d 次", fr.calls)
	}
	if cu.calls != 1 {
		t.Errorf("有 access token 應拉一次 usage，卻 %d 次", cu.calls)
	}
	if _, ok, _ := s.LatestReading("fed"); !ok {
		t.Error("client 餵 token 的帳號 cycle 應寫入 reading")
	}
}

// TestNoTokenAccountSkipped:沒有 access token 的帳號(剛 detach、client 還沒推)整個跳過。
func TestNoTokenAccountSkipped(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	fr := &fakeRefresher{}
	cu := &countingUsage{}
	p := &Poller{Store: s, Usage: cu, OAuth: fr, Now: func() int64 { return 1000 }}

	acct := store.Account{ID: "empty", AccessToken: "", RefreshToken: "", ExpiresAt: 0}
	if err := p.cycle(context.Background(), acct); err != nil {
		t.Fatalf("cycle 不該回錯: %v", err)
	}
	if fr.calls != 0 || cu.calls != 0 {
		t.Errorf("無 token 帳號應全跳過,refresh=%d usage=%d", fr.calls, cu.calls)
	}
	if _, ok, _ := s.LatestReading("empty"); ok {
		t.Error("無 token 帳號不該寫入 reading")
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

package poller

import (
	"context"
	"testing"

	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
)

type fakeUsage struct{ snap usage.Snapshot }

func (f fakeUsage) Fetch(_ context.Context, _ string) (usage.Snapshot, error) { return f.snap, nil }

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

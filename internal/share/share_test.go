package share

import (
	"testing"

	"github.com/ccquota/ccquota/internal/store"
)

type fakeCost struct {
	period float64
	users  map[string]store.UserCost
}

func (f fakeCost) AccountPeriodCost(_ string, _ int64) (float64, error) { return f.period, nil }
func (f fakeCost) UserPeriodCosts(_ string, _ int64) (map[string]store.UserCost, error) {
	return f.users, nil
}

func TestComputeSharePct(t *testing.T) {
	// periodCost 100, 7d=50% → weeklyBudget 200;2 人 → 人均 100。
	// alice 80 → 80%;bob 20 → 20%。
	src := fakeCost{period: 100, users: map[string]store.UserCost{
		"alice": {Cost: 80, Tokens: 800},
		"bob":   {Cost: 20, Tokens: 200},
	}}
	r, err := Compute(src, "main", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if r.WeeklyBudget != 200 || r.PerUserBudget != 100 || r.UserCount != 2 {
		t.Fatalf("反推不對: weekly=%v perUser=%v count=%v", r.WeeklyBudget, r.PerUserBudget, r.UserCount)
	}
	got := map[string]float64{}
	for _, s := range r.Shares {
		got[s.User] = s.SharePct
	}
	if got["alice"] != 80 || got["bob"] != 20 {
		t.Fatalf("佔比不對: %v", got)
	}
}

// TestComputeBelowMinPct:7d% < MinPct → weeklyBudget 0 → 佔比 0(週初不誤報)。
func TestComputeBelowMinPct(t *testing.T) {
	src := fakeCost{period: 100, users: map[string]store.UserCost{"alice": {Cost: 80}}}
	r, _ := Compute(src, "main", 0, 3) // 3% < 5
	if r.WeeklyBudget != 0 || r.PerUserBudget != 0 {
		t.Fatalf("應為 0: weekly=%v perUser=%v", r.WeeklyBudget, r.PerUserBudget)
	}
	if r.Shares[0].SharePct != 0 {
		t.Fatalf("share 應為 0,得 %v", r.Shares[0].SharePct)
	}
}

func TestSinceTS(t *testing.T) {
	r := store.Reading{SevenDayResetsAt: 1_000_000}
	if got := SinceTS(r, true, 9_999_999); got != 1_000_000-7*24*3600 {
		t.Errorf("有 reading 應 reset-7d,得 %d", got)
	}
	if got := SinceTS(store.Reading{}, false, 1_000_000); got != 1_000_000-7*24*3600 {
		t.Errorf("無 reading 應 now-7d,得 %d", got)
	}
}

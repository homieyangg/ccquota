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
	r, err := Compute(src, "main", 0, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.WeeklyBudget != 200 || r.EffectiveBudget != 200 || r.PerUserBudget != 100 || r.UserCount != 2 {
		t.Fatalf("反推不對: weekly=%v eff=%v perUser=%v count=%v", r.WeeklyBudget, r.EffectiveBudget, r.PerUserBudget, r.UserCount)
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
	r, _ := Compute(src, "main", 0, 3, 0) // 3% < 5
	if r.WeeklyBudget != 0 || r.PerUserBudget != 0 {
		t.Fatalf("應為 0: weekly=%v perUser=%v", r.WeeklyBudget, r.PerUserBudget)
	}
	if r.Shares[0].SharePct != 0 {
		t.Fatalf("share 應為 0,得 %v", r.Shares[0].SharePct)
	}
}

// TestComputeWithBaseline:baseline 高於原始反推時,EffectiveBudget / 佔比 改用 baseline。
// 週初 7d% < MinPct(原始反推=0)時也靠 baseline 補,不再整段 0。
func TestComputeWithBaseline(t *testing.T) {
	src := fakeCost{period: 100, users: map[string]store.UserCost{
		"alice": {Cost: 200}, "bob": {Cost: 0},
	}}
	// 原始 weekly=200,baseline=500 → effective=500,2 人 → 人均 250。alice 200/250=80%。
	r, _ := Compute(src, "main", 0, 50, 500)
	if r.WeeklyBudget != 200 {
		t.Fatalf("原始反推應為 200,得 %v", r.WeeklyBudget)
	}
	if r.EffectiveBudget != 500 || r.PerUserBudget != 250 {
		t.Fatalf("effective/人均應 500/250,得 %v/%v", r.EffectiveBudget, r.PerUserBudget)
	}
	got := map[string]float64{}
	for _, s := range r.Shares {
		got[s.User] = s.SharePct
	}
	if got["alice"] != 80 {
		t.Fatalf("alice 佔比應 80,得 %v", got["alice"])
	}

	// 週初:7d%=3% < MinPct → 原始 0,但 baseline 撐住。
	wk, _ := Compute(src, "main", 0, 3, 500)
	if wk.WeeklyBudget != 0 || wk.EffectiveBudget != 500 {
		t.Fatalf("週初應 raw=0 / eff=500,得 %v/%v", wk.WeeklyBudget, wk.EffectiveBudget)
	}
}

// TestUpdateHWM:只在 7d% ≥ BaselineUpdateMinPct 且原始反推更高時才抬高水位。
func TestUpdateHWM(t *testing.T) {
	if got := UpdateHWM(100, 999, 40); got != 100 {
		t.Errorf("7d%% < 50 不該更新,得 %v", got)
	}
	if got := UpdateHWM(100, 200, 60); got != 200 {
		t.Errorf("7d%% ≥ 50 且更高應更新為 200,得 %v", got)
	}
	if got := UpdateHWM(200, 150, 60); got != 200 {
		t.Errorf("不比現有高不該更新,得 %v", got)
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

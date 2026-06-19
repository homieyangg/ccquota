// Package share 計算「每人平分額度」的反推與佔比。
// 抽出供 API(dashboard)與 serve loop(告警)共用,避免兩邊各算一份而分岔。
package share

import (
	"github.com/ccquota/ccquota/internal/calc"
	"github.com/ccquota/ccquota/internal/store"
)

// MinPct:7d% 低於此值代表資料不足以反推,週額度視為 0(週初防亂跳)。
const MinPct = 5.0

// BaselineUpdateMinPct:7d% 低於此值代表離上限還遠、反推不夠準,不更新基準。
const BaselineUpdateMinPct = 50.0

// UpdateHWM 回傳更新後的基準與其對應 7d%。
// 只在 7d% ≥ BaselineUpdateMinPct 且高於記錄時的 7d%(離上限更近=反推更準)才更新。
// 用「最高 7d% 那刻的反推」而非「最大 budget」,讓 Anthropic 突發重置把 7d% 打低時不更新,免灌水。
func UpdateHWM(currentHWM, currentHWMPct, rawDerived, sevenDayPct float64) (hwm, hwmPct float64) {
	if sevenDayPct >= BaselineUpdateMinPct && sevenDayPct > currentHWMPct {
		return rawDerived, sevenDayPct
	}
	return currentHWM, currentHWMPct
}

// SinceTS 推導期間起點:有 reading 用 7d reset - 7天,否則 now - 7天。
// 兩個呼叫端(dashboard / serve loop)都用這個,確保視窗一致。
func SinceTS(reading store.Reading, hasReading bool, now int64) int64 {
	if hasReading {
		return reading.SevenDayResetsAt - 7*24*3600
	}
	return now - 7*24*3600
}

// CostSource 是 share 計算需要的 store 子集。
type CostSource interface {
	AccountPeriodCost(accountID string, sinceTS int64) (float64, error)
	UserPeriodCosts(accountID string, sinceTS int64) (map[string]store.UserCost, error)
}

// UserShare 是單一使用者的份額(未排序)。
type UserShare struct {
	User     string
	Cost     float64
	Tokens   int64
	SharePct float64
}

// Result 是一個帳號的反推結果。
// WeeklyBudget 是當期原始反推(供顯示與更新高水位);EffectiveBudget 取原始與 baseline 較大者,
// 是 PerUserBudget / 佔比實際採用的額度(週初原始為 0 時退回 baseline)。
type Result struct {
	PeriodCost      float64
	WeeklyBudget    float64
	EffectiveBudget float64
	PerUserBudget   float64
	UserCount       int
	Shares          []UserShare
}

// Compute 反推週額度、每人平分額度與各使用者佔比。
// sevenDayPct 來自 reading;sinceTS 用 SinceTS 推導後傳入;baseline 為高水位基準(0 表示無)。
func Compute(s CostSource, accountID string, sinceTS int64, sevenDayPct, baseline float64) (Result, error) {
	periodCost, err := s.AccountPeriodCost(accountID, sinceTS)
	if err != nil {
		return Result{}, err
	}
	userCosts, err := s.UserPeriodCosts(accountID, sinceTS)
	if err != nil {
		return Result{}, err
	}

	weeklyBudget := calc.WeeklyBudget(periodCost, sevenDayPct, MinPct)
	effectiveBudget := max(weeklyBudget, baseline)
	userCount := len(userCosts)
	if userCount < 1 {
		userCount = 1
	}
	perUserBudget := calc.PerUserBudget(effectiveBudget, userCount)

	shares := make([]UserShare, 0, len(userCosts))
	for u, uc := range userCosts {
		shares = append(shares, UserShare{
			User:     u,
			Cost:     uc.Cost,
			Tokens:   uc.Tokens,
			SharePct: calc.SharePct(uc.Cost, perUserBudget),
		})
	}
	return Result{
		PeriodCost:      periodCost,
		WeeklyBudget:    weeklyBudget,
		EffectiveBudget: effectiveBudget,
		PerUserBudget:   perUserBudget,
		UserCount:       userCount,
		Shares:          shares,
	}, nil
}

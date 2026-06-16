// Package share 計算「每人平分額度」的反推與佔比。
// 抽出供 API(dashboard)與 serve loop(告警)共用,避免兩邊各算一份而分岔。
package share

import (
	"github.com/ccquota/ccquota/internal/calc"
	"github.com/ccquota/ccquota/internal/store"
)

// MinPct:7d% 低於此值代表資料不足以反推,週額度視為 0(週初防亂跳)。
const MinPct = 5.0

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
type Result struct {
	PeriodCost    float64
	WeeklyBudget  float64
	PerUserBudget float64
	UserCount     int
	Shares        []UserShare
}

// Compute 反推週額度、每人平分額度與各使用者佔比。
// sevenDayPct 來自 reading;sinceTS 用 SinceTS 推導後傳入。
func Compute(s CostSource, accountID string, sinceTS int64, sevenDayPct float64) (Result, error) {
	periodCost, err := s.AccountPeriodCost(accountID, sinceTS)
	if err != nil {
		return Result{}, err
	}
	userCosts, err := s.UserPeriodCosts(accountID, sinceTS)
	if err != nil {
		return Result{}, err
	}

	weeklyBudget := calc.WeeklyBudget(periodCost, sevenDayPct, MinPct)
	userCount := len(userCosts)
	if userCount < 1 {
		userCount = 1
	}
	perUserBudget := calc.PerUserBudget(weeklyBudget, userCount)

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
		PeriodCost:    periodCost,
		WeeklyBudget:  weeklyBudget,
		PerUserBudget: perUserBudget,
		UserCount:     userCount,
		Shares:        shares,
	}, nil
}

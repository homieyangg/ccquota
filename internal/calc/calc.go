// Package calc 提供純函式用於反推週額度與使用者分配計算。
package calc

// WeeklyBudget 根據期間成本與 7 日使用百分比反推週預算（USD）。
// 若 sevenDayPct < minPct，代表資料不足以反推，回傳 0。
func WeeklyBudget(periodCostUSD, sevenDayPct, minPct float64) float64 {
	if sevenDayPct >= minPct {
		return periodCostUSD / (sevenDayPct / 100)
	}
	return 0
}

// PerUserBudget 將週預算平分給所有使用者。
// userCount < 1 時視為 1；weeklyBudget 為 0 時回傳 0。
func PerUserBudget(weeklyBudget float64, userCount int) float64 {
	if weeklyBudget == 0 {
		return 0
	}
	if userCount < 1 {
		userCount = 1
	}
	return weeklyBudget / float64(userCount)
}

// SharePct 計算使用者成本佔每人份額的百分比。
// perUserBudget 為 0 時回傳 0（避免除以零）。
func SharePct(userCostUSD, perUserBudget float64) float64 {
	if perUserBudget > 0 {
		return userCostUSD / perUserBudget * 100
	}
	return 0
}

package calc_test

import (
	"math"
	"testing"

	"github.com/ccquota/ccquota/internal/calc"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestWeeklyBudget(t *testing.T) {
	const minPct = 5.0
	tests := []struct {
		name        string
		periodCost  float64
		sevenDayPct float64
		want        float64
	}{
		{
			name:        "正常計算：50% 用了 50 USD -> 週額 100 USD",
			periodCost:  50,
			sevenDayPct: 50,
			want:        100,
		},
		{
			name:        "正常計算：25% 用了 10 USD -> 週額 40 USD",
			periodCost:  10,
			sevenDayPct: 25,
			want:        40,
		},
		{
			name:        "低百分比（< minPct）-> 回傳 0",
			periodCost:  5,
			sevenDayPct: 3,
			want:        0,
		},
		{
			name:        "恰好等於 minPct -> 正常計算",
			periodCost:  5,
			sevenDayPct: 5,
			want:        100,
		},
		{
			name:        "零成本、正常% -> 回傳 0",
			periodCost:  0,
			sevenDayPct: 50,
			want:        0,
		},
		{
			name:        "零百分比 (< minPct) -> 回傳 0",
			periodCost:  100,
			sevenDayPct: 0,
			want:        0,
		},
		{
			name:        "100% -> 週額等於成本",
			periodCost:  75,
			sevenDayPct: 100,
			want:        75,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calc.WeeklyBudget(tc.periodCost, tc.sevenDayPct, minPct)
			if !almostEqual(got, tc.want) {
				t.Errorf("WeeklyBudget(%v, %v, %v) = %v, want %v",
					tc.periodCost, tc.sevenDayPct, minPct, got, tc.want)
			}
		})
	}
}

func TestPerUserBudget(t *testing.T) {
	tests := []struct {
		name         string
		weeklyBudget float64
		userCount    int
		want         float64
	}{
		{
			name:         "正常：100 USD / 4 users = 25",
			weeklyBudget: 100,
			userCount:    4,
			want:         25,
		},
		{
			name:         "零使用者 -> 視為 1",
			weeklyBudget: 60,
			userCount:    0,
			want:         60,
		},
		{
			name:         "負使用者 -> 視為 1",
			weeklyBudget: 60,
			userCount:    -1,
			want:         60,
		},
		{
			name:         "零預算 -> 回傳 0",
			weeklyBudget: 0,
			userCount:    5,
			want:         0,
		},
		{
			name:         "1 user -> 等於週預算",
			weeklyBudget: 80,
			userCount:    1,
			want:         80,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calc.PerUserBudget(tc.weeklyBudget, tc.userCount)
			if !almostEqual(got, tc.want) {
				t.Errorf("PerUserBudget(%v, %v) = %v, want %v",
					tc.weeklyBudget, tc.userCount, got, tc.want)
			}
		})
	}
}

func TestTokenSharePct(t *testing.T) {
	tests := []struct {
		name        string
		userTokens  int64
		totalTokens int64
		want        float64
	}{
		{
			name:        "正常：270M / 288M ≈ 93.8%",
			userTokens:  270083820,
			totalTokens: 270083820 + 17844745,
			want:        93.8023707,
		},
		{
			name:        "另一半：17.8M / 288M ≈ 6.2%（與上一筆相加=100%）",
			userTokens:  17844745,
			totalTokens: 270083820 + 17844745,
			want:        6.1976293,
		},
		{
			name:        "獨佔 -> 100%",
			userTokens:  500,
			totalTokens: 500,
			want:        100,
		},
		{
			name:        "總量為 0 -> 回傳 0（避免除以零）",
			userTokens:  0,
			totalTokens: 0,
			want:        0,
		},
		{
			name:        "零用量 -> 0%",
			userTokens:  0,
			totalTokens: 1000,
			want:        0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calc.TokenSharePct(tc.userTokens, tc.totalTokens)
			if math.Abs(got-tc.want) > 1e-4 {
				t.Errorf("TokenSharePct(%v, %v) = %v, want %v",
					tc.userTokens, tc.totalTokens, got, tc.want)
			}
		})
	}
}

func TestSharePct(t *testing.T) {
	tests := []struct {
		name          string
		userCost      float64
		perUserBudget float64
		want          float64
	}{
		{
			name:          "正常：用了 25 USD / 100 USD per-user = 25%",
			userCost:      25,
			perUserBudget: 100,
			want:          25,
		},
		{
			name:          "超過 100%",
			userCost:      150,
			perUserBudget: 100,
			want:          150,
		},
		{
			name:          "零 perUserBudget -> 回傳 0",
			userCost:      50,
			perUserBudget: 0,
			want:          0,
		},
		{
			name:          "零成本 -> 0%",
			userCost:      0,
			perUserBudget: 100,
			want:          0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calc.SharePct(tc.userCost, tc.perUserBudget)
			if !almostEqual(got, tc.want) {
				t.Errorf("SharePct(%v, %v) = %v, want %v",
					tc.userCost, tc.perUserBudget, got, tc.want)
			}
		})
	}
}

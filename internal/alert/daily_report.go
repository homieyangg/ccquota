package alert

import (
	"context"
	"fmt"
	"html"
	"log"
	"sort"
	"strings"
)

// DailyReportData 是每日用量報告所需的預算與 per-user 資料。
type DailyReportData struct {
	NowTPE       string // "06/20 18:00"
	DashboardURL string
	Accounts     []DailyReportAccount
}

// DailyReportAccount 是單一帳號的報告資料。
type DailyReportAccount struct {
	ID            string
	HasReading    bool
	SevenDay      float64
	FiveHour      float64
	Stale         bool
	WeeklyBudget  float64
	PerUserBudget float64
	Users         []DailyReportUser
}

// DailyReportUser 是單一使用者在報告中的用量資料。
type DailyReportUser struct {
	Name       string
	PeriodCost float64
	TodayCost  float64
	Tokens     int64
	SharePct   float64
}

// DailyReport 對每個 sink 組裝並送出每日用量報告。
func (n *Notifier) DailyReport(ctx context.Context, data DailyReportData) error {
	if len(n.sinks) == 0 {
		return nil
	}
	var lastErr error
	for _, s := range n.sinks {
		lang := s.Lang()
		if lang == "" {
			lang = n.cfg.lang()
		}
		text := buildDailyReport(lang, data, n.cfg.weeklyWarn(), n.cfg.weeklyCrit())
		if _, err := s.Send(ctx, text); err != nil {
			log.Printf("daily report send error: %v", err)
			lastErr = err
		}
	}
	return lastErr
}

type dailyLabels struct {
	title     string
	time      string
	account   string
	shared    string
	noReading string
	budget    string
	perUser   string
	period    string
	share     string
	used      string
	today     string
	tokens    string
	dashboard string
}

var labelsByLang = map[string]dailyLabels{
	"en": {
		title:     "Daily Usage Report",
		time:      "Taipei",
		account:   "Account",
		shared:    "shared",
		noReading: "No account quota reading",
		budget:    "Est. weekly budget",
		perUser:   "per user",
		period:    "this period",
		share:     "share",
		used:      "used",
		today:     "today",
		tokens:    "work tokens",
		dashboard: "View dashboard",
	},
	"zh-TW": {
		title:     "每日用量報告",
		time:      "台北",
		account:   "帳號",
		shared:    "整個帳號共用",
		noReading: "尚無帳號額度讀數",
		budget:    "反推整週額度",
		perUser:   "每人平分",
		period:    "本週期",
		share:     "平分",
		used:      "用了",
		today:     "今日",
		tokens:    "工作 token",
		dashboard: "查看 dashboard",
	},
	"zh-CN": {
		title:     "每日用量报告",
		time:      "台北",
		account:   "账号",
		shared:    "整个账号共用",
		noReading: "尚无账号额度读数",
		budget:    "反推整周额度",
		perUser:   "每人均摊",
		period:    "本周期",
		share:     "均摊",
		used:      "用了",
		today:     "今日",
		tokens:    "工作 token",
		dashboard: "查看 dashboard",
	},
}

func getLabels(lang string) dailyLabels {
	if l, ok := labelsByLang[lang]; ok {
		return l
	}
	return labelsByLang["en"]
}

func buildDailyReport(lang string, data DailyReportData, warnPct, critPct float64) string {
	l := getLabels(lang)
	var b strings.Builder

	b.WriteString(fmt.Sprintf("📊 <b>%s</b>（%s %s）", l.title, l.time, data.NowTPE))

	for _, a := range data.Accounts {
		if a.HasReading {
			bar := weeklyBar(a.SevenDay, warnPct, critPct)
			stale := ""
			if a.Stale {
				stale = " ⏳"
			}
			b.WriteString(fmt.Sprintf("\n%s %s 7d <b>%.0f%%</b>%s  ·  5h %.0f%%（%s）",
				bar, l.account, a.SevenDay, stale, a.FiveHour, l.shared))
		} else {
			b.WriteString(fmt.Sprintf("\n⚪ %s", l.noReading))
		}

		b.WriteString(fmt.Sprintf("\n%s ≈ $%s，%s ≈ $%s",
			l.budget, fmtBudget(a.WeeklyBudget),
			l.perUser, fmtBudget(a.PerUserBudget)))

		users := make([]DailyReportUser, len(a.Users))
		copy(users, a.Users)
		sort.Slice(users, func(i, j int) bool {
			return users[i].PeriodCost > users[j].PeriodCost
		})

		for _, u := range users {
			shareTxt := "-"
			if a.PerUserBudget > 0 {
				shareTxt = fmt.Sprintf("%.0f%%", u.SharePct)
			}
			b.WriteString(fmt.Sprintf("\n\n• <b>%s</b>  %s $%s / %s $%s（%s %s）",
				html.EscapeString(u.Name),
				l.period, fmtCost(u.PeriodCost),
				l.share, fmtBudget(a.PerUserBudget),
				l.used, shareTxt))
			b.WriteString(fmt.Sprintf("\n   %s $%s  ·  %s %s",
				l.today, fmtCost(u.TodayCost),
				l.tokens, fmtTokens(u.Tokens)))
		}
	}

	if data.DashboardURL != "" {
		b.WriteString(fmt.Sprintf("\n\n<a href=\"%s\">%s</a>", data.DashboardURL, l.dashboard))
	}

	return b.String()
}

func weeklyBar(pct, warnPct, critPct float64) string {
	if pct >= critPct {
		return "🔴"
	}
	if pct >= warnPct {
		return "🟡"
	}
	return "🟢"
}

func fmtCost(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

func fmtBudget(v float64) string {
	return fmt.Sprintf("%.0f", v)
}

func fmtTokens(v int64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(v)/1_000_000)
	case v >= 1000:
		return fmt.Sprintf("%.1fk", float64(v)/1000)
	default:
		return fmt.Sprintf("%d", v)
	}
}

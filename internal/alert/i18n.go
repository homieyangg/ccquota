package alert

import "fmt"

// templates 是三語系的訊息模板。
// 每個 key 對應一種事件，各語系提供 fmt.Sprintf 格式串。
//
// Placeholders（依序）：
//
//	reset           → %s account, %.0f from%, %.0f to%
//	weekly_warn     → %s account, %.0f pct%
//	weekly_crit     → %s account, %.0f pct%
//	five_hour_crit  → %s account, %.0f pct%
//	stale           → %s account, %d ageSec
var templates = map[string]map[string]string{
	"en": {
		"reset":          "🔄 <b>Quota Reset</b>\nAccount: <code>%s</code>\n7-day usage reset: <b>%.0f%%</b> → <b>%.0f%%</b>",
		"weekly_warn":    "🟡 <b>Weekly Quota Warning</b>\nAccount: <code>%s</code>\n7-day usage at <b>%.0f%%</b> — approaching limit",
		"weekly_crit":    "🚨 <b>Weekly Quota Critical</b>\nAccount: <code>%s</code>\n7-day usage at <b>%.0f%%</b> — near limit",
		"five_hour_crit": "🚨 <b>5-Hour Quota Critical</b>\nAccount: <code>%s</code>\n5-hour usage at <b>%.0f%%</b> — near limit",
		"stale":          "⚠️ <b>Poller Stale</b>\nAccount: <code>%s</code>\nNo data for %d seconds — poller may be down",
	},
	"zh-TW": {
		"reset":          "🔄 <b>配額已重置</b>\n帳號：<code>%s</code>\n7日用量重置：<b>%.0f%%</b> → <b>%.0f%%</b>",
		"weekly_warn":    "🟡 <b>週配額警告</b>\n帳號：<code>%s</code>\n7日用量已達 <b>%.0f%%</b>，接近上限",
		"weekly_crit":    "🚨 <b>週配額緊急</b>\n帳號：<code>%s</code>\n7日用量已達 <b>%.0f%%</b>，即將觸頂",
		"five_hour_crit": "🚨 <b>5小時配額緊急</b>\n帳號：<code>%s</code>\n5小時用量已達 <b>%.0f%%</b>，即將觸頂",
		"stale":          "⚠️ <b>Poller 停止回報</b>\n帳號：<code>%s</code>\n已 %d 秒無資料，poller 可能已停止",
	},
	"zh-CN": {
		"reset":          "🔄 <b>配额已重置</b>\n账号：<code>%s</code>\n7日用量重置：<b>%.0f%%</b> → <b>%.0f%%</b>",
		"weekly_warn":    "🟡 <b>周配额警告</b>\n账号：<code>%s</code>\n7日用量已达 <b>%.0f%%</b>，接近上限",
		"weekly_crit":    "🚨 <b>周配额紧急</b>\n账号：<code>%s</code>\n7日用量已达 <b>%.0f%%</b>，即将触顶",
		"five_hour_crit": "🚨 <b>5小时配额紧急</b>\n账号：<code>%s</code>\n5小时用量已达 <b>%.0f%%</b>，即将触顶",
		"stale":          "⚠️ <b>Poller 停止上报</b>\n账号：<code>%s</code>\n已 %d 秒无数据，poller 可能已停止",
	},
}

// renderTemplate 用 lang 渲染指定 key 的訊息。
// 若 lang 或 key 不存在則 fallback 到 "en"。
func renderTemplate(lang, key string, args ...any) string {
	langMap, ok := templates[lang]
	if !ok {
		langMap = templates["en"]
	}
	tpl, ok := langMap[key]
	if !ok {
		tpl = templates["en"][key]
	}
	return fmt.Sprintf(tpl, args...)
}

package alert

import (
	"context"
	"fmt"
	"html"
	"log"

	"github.com/ccquota/ccquota/internal/store"
)

// AlertStore 是 Notifier 依賴的 store 方法子集（方便測試用 fake）。
type AlertStore interface {
	AlertAlreadyFired(account, kind, windowKey string) (bool, error)
	MarkAlertFired(account, kind, windowKey string, ts int64) error
	GetAlertMessage(account, window, sinkKey string) (store.AlertMessage, bool, error)
	UpsertAlertMessage(account, window, sinkKey, ref, tier string) error
}

// Config 持有 Notifier 的設定。零值等同預設值。
type Config struct {
	Lang            string  // "en" | "zh-TW" | "zh-CN"；預設 "en"（per-channel 語言為空時的 fallback）
	WeeklyWarn      float64 // 7d warn 閾值%；預設 75
	WeeklyCrit      float64 // 7d crit 閾值%；預設 90
	FiveHourCrit    float64 // 5h crit 閾值%；預設 95
	PollerStaleSec  int64   // stale 判斷秒數；預設 1800
	UserShareNotify bool    // 是否啟用每人平分額度告警；預設 false
	UserShareWarn   float64 // 平分額度 warn 閾值%；預設 150
	UserShareCrit   float64 // 平分額度 crit 閾值%；預設 250
}

// orDefault 在 v 為零值時回傳 def，用來把 Config 零值欄位映射成預設值。
func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

func (c *Config) lang() string           { return orDefault(c.Lang, "en") }
func (c *Config) weeklyWarn() float64    { return orDefault(c.WeeklyWarn, 75) }
func (c *Config) weeklyCrit() float64    { return orDefault(c.WeeklyCrit, 90) }
func (c *Config) fiveHourCrit() float64  { return orDefault(c.FiveHourCrit, 95) }
func (c *Config) pollerStaleSec() int64  { return orDefault(c.PollerStaleSec, 1800) }
func (c *Config) userShareWarn() float64 { return orDefault(c.UserShareWarn, 150) }
func (c *Config) userShareCrit() float64 { return orDefault(c.UserShareCrit, 250) }

// Notifier 發送各類通知至所有已設定的 Sink。
// 無 Sink 時所有方法為 no-op（安全）。
type Notifier struct {
	cfg   Config
	store AlertStore
	sinks []Sink
}

// NewNotifier 建立 Notifier。sinks 可為空。
func NewNotifier(cfg Config, store AlertStore, sinks ...Sink) *Notifier {
	return &Notifier{cfg: cfg, store: store, sinks: sinks}
}

// RenderTemplate 是 renderTemplate 的 exported wrapper，供測試使用。
func RenderTemplate(lang, key string, args ...any) string {
	return renderTemplate(lang, key, args...)
}

// sendRendered 對每個 sink 用「該 sink 的語言」渲染同一個 key 後送出。
// sink 語言為空時 fallback 到全域 Config.Lang(再空則 en)。
// 任一失敗記 log 並繼續,回傳 lastErr;呼叫端只在 nil 時 mark,維持原 dedup 契約
// (全部 sink 嘗試後才回,部分失敗不 mark → 下輪重送)。
func (n *Notifier) sendRendered(ctx context.Context, key string, args ...any) error {
	var lastErr error
	for _, s := range n.sinks {
		lang := s.Lang()
		if lang == "" {
			lang = n.cfg.lang()
		}
		if _, err := s.Send(ctx, renderTemplate(lang, key, args...)); err != nil {
			log.Printf("alert send error: %v", err)
			lastErr = err
		}
	}
	return lastErr
}

// windowAnchor 把視窗錨點(resets_at,epoch 秒)四捨五入到最近的分鐘。
// OAuth 端的 sub-second timestamp 轉 epoch 會讓 resets_at 在相鄰兩值間 ±1s 抖動,
// dedup key 直接吃原始秒數的話一抖就變成新 key → 同一視窗的告警重送。錨到分鐘可吸收抖動。
func windowAnchor(epoch int64) int64 {
	return (epoch + 30) / 60
}

// fired 查詢 dedup，若已觸發回傳 true（error 時視為未觸發，保守策略）。
func (n *Notifier) fired(account, kind, windowKey string) bool {
	ok, err := n.store.AlertAlreadyFired(account, kind, windowKey)
	if err != nil {
		log.Printf("alert dedup check error: %v", err)
		return false
	}
	return ok
}

// mark 標記已觸發，ts 傳 0 時不影響功能（無 wall clock 依賴）。
func (n *Notifier) mark(account, kind, windowKey string, ts int64) {
	if err := n.store.MarkAlertFired(account, kind, windowKey, ts); err != nil {
		log.Printf("alert mark error: %v", err)
	}
}

// sendOnce 以 (account, kind, windowKey) 做 dedup：已觸發過就跳過，否則送出並標記。
// 成功送出才 mark，部分 sink 失敗（sendRendered 回 lastErr）則不 mark，留待下輪重送。
func (n *Notifier) sendOnce(ctx context.Context, account, kind, windowKey, tmplKey string, args ...any) error {
	if n.fired(account, kind, windowKey) {
		return nil
	}
	if err := n.sendRendered(ctx, tmplKey, args...); err != nil {
		return err
	}
	n.mark(account, kind, windowKey, 0)
	return nil
}

// Reset 發送 7d quota reset 通知（不做 dedup，每次 OnReset 均送）。
func (n *Notifier) Reset(ctx context.Context, account string, from, to float64) error {
	if len(n.sinks) == 0 {
		return nil
	}
	return n.sendRendered(ctx, "reset", account, from, to)
}

// tierRank 給告警層級排序,供「已達同層級或更高就不重送」判斷。
func tierRank(tier string) int {
	switch tier {
	case "crit":
		return 2
	case "warn":
		return 1
	default:
		return 0
	}
}

// Thresholds 根據 sevenDay / fiveHour 百分比決定是否發 warn/crit 通知。
// weekly 同一視窗只有一則:warn 升 crit 時就地編輯既有訊息(不分兩則),由 weeklyEscalate 處理。
// weeklyBudget / periodCost 用於 weekly 文案(反推額度、本週剩餘);5h crit 獨立判斷,沿用 alert_state dedup。
func (n *Notifier) Thresholds(ctx context.Context, account string, sevenDay, fiveHour, weeklyBudget, periodCost float64, sevenDayResetsAt, fiveHourResetsAt int64) error {
	if len(n.sinks) == 0 {
		return nil
	}

	window := fmt.Sprintf("%d", windowAnchor(sevenDayResetsAt))
	remaining := weeklyBudget - periodCost
	if remaining < 0 {
		remaining = 0
	}
	if sevenDay >= n.cfg.weeklyCrit() {
		n.weeklyEscalate(ctx, account, window, "crit", "weekly_crit", sevenDay, weeklyBudget, remaining)
	} else if sevenDay >= n.cfg.weeklyWarn() {
		n.weeklyEscalate(ctx, account, window, "warn", "weekly_warn", sevenDay, weeklyBudget, remaining)
	}

	if fiveHour >= n.cfg.fiveHourCrit() {
		wk := fmt.Sprintf("%d", windowAnchor(fiveHourResetsAt))
		if err := n.sendOnce(ctx, account, "five_hour", wk, "five_hour_crit", account, fiveHour); err != nil {
			return err
		}
	}

	return nil
}

// weeklyEscalate 對每個 sink 維護「該視窗一則訊息」:首次達標 Send,升級時就地 Edit。
// 已達同層級或更高 → 跳過。Edit 失敗或 sink 不支援 → 退化成重送一則。
// budgetSuffix 依語言渲染、budget≤0 時為空,避免顯示 $0。
func (n *Notifier) weeklyEscalate(ctx context.Context, account, window, tier, tmplKey string, sevenDay, weeklyBudget, remaining float64) {
	for _, s := range n.sinks {
		key := s.Key()
		prev, ok, err := n.store.GetAlertMessage(account, window, key)
		if err != nil {
			log.Printf("alert weekly get-message error: %v", err)
			continue
		}
		if ok && tierRank(prev.Tier) >= tierRank(tier) {
			continue
		}
		lang := s.Lang()
		if lang == "" {
			lang = n.cfg.lang()
		}
		text := renderTemplate(lang, tmplKey, account, sevenDay, weeklyBudgetSuffix(lang, weeklyBudget, remaining))
		if ok && prev.Ref != "" {
			if editErr := s.Edit(ctx, prev.Ref, text); editErr == nil {
				n.mustUpsertMsg(account, window, key, prev.Ref, tier)
				continue
			}
		}
		ref, sendErr := s.Send(ctx, text)
		if sendErr != nil {
			log.Printf("alert weekly send error: %v", sendErr)
			continue
		}
		n.mustUpsertMsg(account, window, key, ref, tier)
	}
}

// weeklyBudgetSuffix 依語言組「反推額度/本週剩餘」尾段;budget≤0 回空字串。
func weeklyBudgetSuffix(lang string, weeklyBudget, remaining float64) string {
	if weeklyBudget <= 0 {
		return ""
	}
	return renderTemplate(lang, "weekly_budget_suffix", weeklyBudget, remaining)
}

func (n *Notifier) mustUpsertMsg(account, window, key, ref, tier string) {
	if err := n.store.UpsertAlertMessage(account, window, key, ref, tier); err != nil {
		log.Printf("alert weekly upsert-message error: %v", err)
	}
}

// Stale 若 ageSec >= pollerStaleSec 則發送 stale 警報，以 6h bucket dedup 節流。
func (n *Notifier) Stale(ctx context.Context, account string, ageSec int64) error {
	if len(n.sinks) == 0 {
		return nil
	}
	if ageSec < n.cfg.pollerStaleSec() {
		return nil
	}
	bucket := ageSec / 21600 // 6h bucket
	wk := fmt.Sprintf("%d", bucket)
	return n.sendOnce(ctx, account, "stale", wk, "stale", account, ageSec)
}

// PollerStaleSec 回傳 stale 判斷閾值秒數（供 main.go 使用）。
func (n *Notifier) PollerStaleSec() int64 {
	return n.cfg.pollerStaleSec()
}

// UserShareReading 是單一使用者的平分額度狀態。
type UserShareReading struct {
	User     string
	SharePct float64
	Cost     float64
}

// UserShareThresholds 對每個使用者比對「平分額度」warn/crit（advisory，非帳號 throttle）。
//   - 預設關閉：UserShareNotify=false 直接 no-op。
//   - sevenDayResetsAt<=0 時跳過（無有效視窗錨點，避免 dedup 永久卡死）。
//   - 每 user 只送最高層級（crit 優先 warn），dedup 綁 (user, tier, 7d 視窗)，重置自動 re-arm。
//   - username 為自由字串，HTML-escape 後再進 HTML 模板。
func (n *Notifier) UserShareThresholds(ctx context.Context, account string, users []UserShareReading, perUserBudget float64, sevenDayResetsAt int64) error {
	if !n.cfg.UserShareNotify || len(n.sinks) == 0 {
		return nil
	}
	if sevenDayResetsAt <= 0 {
		return nil
	}
	for _, u := range users {
		var tmplKey, tier string
		switch {
		case u.SharePct >= n.cfg.userShareCrit():
			tmplKey, tier = "user_share_crit", "crit"
		case u.SharePct >= n.cfg.userShareWarn():
			tmplKey, tier = "user_share_warn", "warn"
		default:
			continue
		}
		// window_key 不會被 parse、只比較；user 放末段，內含 ':' 也無害。
		wk := fmt.Sprintf("%d:%s:%s", windowAnchor(sevenDayResetsAt), tier, u.User)
		safeUser := html.EscapeString(u.User)
		if err := n.sendOnce(ctx, account, "user_share", wk, tmplKey, safeUser, u.SharePct, u.Cost, perUserBudget); err != nil {
			return err
		}
	}
	return nil
}

package alert

import (
	"context"
	"fmt"
	"log"
)

// AlertStore 是 Notifier 依賴的 store 方法子集（方便測試用 fake）。
type AlertStore interface {
	AlertAlreadyFired(account, kind, windowKey string) (bool, error)
	MarkAlertFired(account, kind, windowKey string, ts int64) error
}

// Config 持有 Notifier 的設定。零值等同預設值。
type Config struct {
	Lang           string  // "en" | "zh-TW" | "zh-CN"；預設 "en"
	WeeklyWarn     float64 // 7d warn 閾值%；預設 75
	WeeklyCrit     float64 // 7d crit 閾值%；預設 90
	FiveHourCrit   float64 // 5h crit 閾值%；預設 95
	PollerStaleSec int64   // stale 判斷秒數；預設 1800
}

func (c *Config) lang() string {
	if c.Lang == "" {
		return "en"
	}
	return c.Lang
}

func (c *Config) weeklyWarn() float64 {
	if c.WeeklyWarn == 0 {
		return 75
	}
	return c.WeeklyWarn
}

func (c *Config) weeklyCrit() float64 {
	if c.WeeklyCrit == 0 {
		return 90
	}
	return c.WeeklyCrit
}

func (c *Config) fiveHourCrit() float64 {
	if c.FiveHourCrit == 0 {
		return 95
	}
	return c.FiveHourCrit
}

func (c *Config) pollerStaleSec() int64 {
	if c.PollerStaleSec == 0 {
		return 1800
	}
	return c.PollerStaleSec
}

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

// sendAll 依序送給所有 sinks，任一失敗記 log 但繼續。
func (n *Notifier) sendAll(ctx context.Context, text string) error {
	var lastErr error
	for _, s := range n.sinks {
		if err := s.Send(ctx, text); err != nil {
			log.Printf("alert send error: %v", err)
			lastErr = err
		}
	}
	return lastErr
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

// Reset 發送 7d quota reset 通知（不做 dedup，每次 OnReset 均送）。
func (n *Notifier) Reset(ctx context.Context, account string, from, to float64) error {
	if len(n.sinks) == 0 {
		return nil
	}
	text := renderTemplate(n.cfg.lang(), "reset", account, from, to)
	return n.sendAll(ctx, text)
}

// Thresholds 根據 sevenDay / fiveHour 百分比決定是否發 warn/crit 通知。
// 7d 只送最高層級（crit 優先於 warn）；5h crit 獨立判斷。
// 透過 store dedup，每個視窗每個層級只送一次。
func (n *Notifier) Thresholds(ctx context.Context, account string, sevenDay, fiveHour float64, sevenDayResetsAt, fiveHourResetsAt int64) error {
	if len(n.sinks) == 0 {
		return nil
	}

	// 7d：判斷最高層級（crit > warn）
	if sevenDay >= n.cfg.weeklyCrit() {
		wk := fmt.Sprintf("%d:crit", sevenDayResetsAt)
		if !n.fired(account, "weekly", wk) {
			text := renderTemplate(n.cfg.lang(), "weekly_crit", account, sevenDay)
			if err := n.sendAll(ctx, text); err != nil {
				return err
			}
			n.mark(account, "weekly", wk, 0)
		}
	} else if sevenDay >= n.cfg.weeklyWarn() {
		wk := fmt.Sprintf("%d:warn", sevenDayResetsAt)
		if !n.fired(account, "weekly", wk) {
			text := renderTemplate(n.cfg.lang(), "weekly_warn", account, sevenDay)
			if err := n.sendAll(ctx, text); err != nil {
				return err
			}
			n.mark(account, "weekly", wk, 0)
		}
	}

	// 5h crit
	if fiveHour >= n.cfg.fiveHourCrit() {
		wk := fmt.Sprintf("%d", fiveHourResetsAt)
		if !n.fired(account, "five_hour", wk) {
			text := renderTemplate(n.cfg.lang(), "five_hour_crit", account, fiveHour)
			if err := n.sendAll(ctx, text); err != nil {
				return err
			}
			n.mark(account, "five_hour", wk, 0)
		}
	}

	return nil
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
	if n.fired(account, "stale", wk) {
		return nil
	}
	text := renderTemplate(n.cfg.lang(), "stale", account, ageSec)
	if err := n.sendAll(ctx, text); err != nil {
		return err
	}
	n.mark(account, "stale", wk, 0)
	return nil
}

// PollerStaleSec 回傳 stale 判斷閾值秒數（供 main.go 使用）。
func (n *Notifier) PollerStaleSec() int64 {
	return n.cfg.pollerStaleSec()
}

package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
)

type UsageFetcher interface {
	Fetch(ctx context.Context, accessToken string) (usage.Snapshot, error)
}

// Refresher 換取新的 access token。*oauth.Client 滿足此介面。
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (oauth.Token, error)
}

type Poller struct {
	Store         *store.Store
	Usage         UsageFetcher
	OAuth         Refresher    // may be nil in tests that never refresh
	DropPct       float64      // default 5
	RefreshBuffer int64        // seconds before expiry to refresh; default 3600
	MinAdvanceSec int64        // minimum resets_at advance to count as a natural reset; default 3600
	MinBackoff    int64        // refresh 失敗後最短退避秒數;預設 600
	MaxBackoff    int64        // 退避上限秒數;預設 21600(6h)
	Now           func() int64 // default time.Now().Unix
	OnReset       func(account string, from, to float64)

	mu      sync.Mutex
	gate    map[string]int64 // accountID -> 在此 unix 時間前不再嘗試 refresh
	backoff map[string]int64 // accountID -> 目前退避秒數
}

func (p *Poller) minBackoff() int64 {
	if p.MinBackoff > 0 {
		return p.MinBackoff
	}
	return 600
}

func (p *Poller) maxBackoff() int64 {
	if p.MaxBackoff > 0 {
		return p.MaxBackoff
	}
	return 21600
}

// refreshAllowed 回報目前是否可嘗試 refresh(退避窗口外)。
func (p *Poller) refreshAllowed(id string, now int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return now >= p.gate[id] // nil map 讀回 0,等同允許
}

// noteRefreshFail 記一次 refresh 失敗,指數加大退避並設下次可嘗試時間。回傳本次退避秒數。
func (p *Poller) noteRefreshFail(id string, now int64) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.backoff == nil {
		p.backoff = map[string]int64{}
		p.gate = map[string]int64{}
	}
	d := p.backoff[id] * 2
	if d < p.minBackoff() {
		d = p.minBackoff()
	}
	if d > p.maxBackoff() {
		d = p.maxBackoff()
	}
	p.backoff[id] = d
	p.gate[id] = now + d
	return d
}

// noteRefreshOK 清掉退避狀態。
func (p *Poller) noteRefreshOK(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.backoff, id)
	delete(p.gate, id)
}

func (p *Poller) now() int64 {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now().Unix()
}

// cycle polls a single account once.
func (p *Poller) cycle(ctx context.Context, a store.Account) error {
	now := p.now()
	buf := p.RefreshBuffer
	if buf == 0 {
		buf = 3600
	}
	// 只有「自己保管 refresh token」的帳號才自行 refresh;client 餵 token 的帳號
	// (refresh token 為空)永不碰被限流的 token endpoint,access token 由
	// POST /v1/token 餵入。退避避免限流時每輪狂重試。
	if p.OAuth != nil && a.RefreshToken != "" && a.ExpiresAt-now < buf {
		if !p.refreshAllowed(a.ID, now) {
			log.Printf("account %s: refresh backing off, skip this cycle", a.ID)
		} else {
			tok, err := p.OAuth.Refresh(ctx, a.RefreshToken)
			if err != nil {
				d := p.noteRefreshFail(a.ID, now)
				if now >= a.ExpiresAt {
					return fmt.Errorf("account %s: token expired, refresh failed, backing off %ds: %w", a.ID, d, err)
				}
				log.Printf("account %s: refresh failed, keep token (%ds left), backing off %ds: %v", a.ID, a.ExpiresAt-now, d, err)
			} else {
				p.noteRefreshOK(a.ID)
				a.AccessToken = tok.AccessToken
				a.RefreshToken = tok.RefreshToken
				a.ExpiresAt = now + tok.ExpiresIn
				if err := p.Store.UpsertAccount(a); err != nil {
					return err
				}
			}
		}
	}

	// 沒有可用 access token 就跳過(例如剛 detach、client 還沒推 token)。
	if a.AccessToken == "" {
		return nil
	}

	snap, err := p.Usage.Fetch(ctx, a.AccessToken)
	if err != nil {
		return err
	}

	prev, hadPrev, err := p.Store.LatestReading(a.ID)
	if err != nil {
		return err
	}

	if err := p.Store.InsertReading(store.Reading{
		AccountID: a.ID, TS: now,
		SevenDay: snap.SevenDay, FiveHour: snap.FiveHour, Sonnet: snap.Sonnet, Opus: snap.Opus,
		SevenDayResetsAt: snap.SevenDayResetsAt, FiveHourResetsAt: snap.FiveHourResetsAt,
	}); err != nil {
		return err
	}

	if hadPrev {
		drop := p.DropPct
		if drop == 0 {
			drop = 5
		}
		minAdv := p.MinAdvanceSec
		if minAdv == 0 {
			minAdv = 3600
		}
		if DetectReset(prev.SevenDay, prev.SevenDayResetsAt, snap.SevenDay, snap.SevenDayResetsAt, drop, minAdv) {
			detail, _ := json.Marshal(map[string]float64{"from": prev.SevenDay, "to": snap.SevenDay})
			if err := p.Store.InsertEvent(a.ID, now, "reset", string(detail)); err != nil {
				return err
			}
			if p.OnReset != nil {
				p.OnReset(a.ID, prev.SevenDay, snap.SevenDay)
			}
		}
	}
	return nil
}

// PollAll runs one cycle for every account, logging per-account errors.
func (p *Poller) PollAll(ctx context.Context) {
	accts, err := p.Store.ListAccounts()
	if err != nil {
		log.Printf("list accounts: %v", err)
		return
	}
	for _, a := range accts {
		if err := p.cycle(ctx, a); err != nil {
			log.Printf("account %s: %v", a.ID, err)
		}
	}
}

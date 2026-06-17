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
	RefreshCmd    func(ctx context.Context) error // CLI-backed 帳號快到期時呼叫(跑 claude doctor);nil 則略過

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
// credsRefreshAhead:CLI-backed 帳號剩餘壽命低於此(秒)就跑 claude doctor 觸發 refresh。
// 取 10 分,涵蓋實測的 doctor refresh buffer(<10 分)。
const credsRefreshAhead = 600

func (p *Poller) cycle(ctx context.Context, a store.Account) error {
	now := p.now()

	// CLI-backed 帳號:直接讀本機 claude creds 檔取 token,快到期時跑 claude doctor
	// 觸發免費 refresh(不碰被限流的 token endpoint、不叫 model)。
	if a.CredsPath != "" {
		return p.cycleCLIBacked(ctx, a, now)
	}

	buf := p.RefreshBuffer
	if buf == 0 {
		buf = 3600
	}
	// 只有「自己保管 refresh token」的帳號才自行 refresh;沒有 refresh token 的帳號
	// (例如 CLI-backed)不碰被限流的 token endpoint。退避避免限流時每輪狂重試。
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
	return p.recordUsage(ctx, a, now, a.AccessToken)
}

// cycleCLIBacked 處理 CLI-backed 帳號:讀本機 creds、快到期觸發 doctor refresh、拉 usage。
func (p *Poller) cycleCLIBacked(ctx context.Context, a store.Account, now int64) error {
	token, exp, err := readCredsToken(a.CredsPath)
	if err != nil {
		return fmt.Errorf("account %s: read creds %s: %w", a.ID, a.CredsPath, err)
	}
	if exp-now < credsRefreshAhead && p.RefreshCmd != nil {
		if err := p.RefreshCmd(ctx); err != nil {
			log.Printf("account %s: refresh cmd: %v", a.ID, err)
		} else {
			token, _, _ = readCredsToken(a.CredsPath)
		}
	}
	if token == "" {
		return fmt.Errorf("account %s: creds %s has no access token", a.ID, a.CredsPath)
	}
	return p.recordUsage(ctx, a, now, token)
}

// recordUsage 用 token 拉 usage、寫 reading、偵測重置。一般 cycle 與 CLI-backed 共用。
func (p *Poller) recordUsage(ctx context.Context, a store.Account, now int64, token string) error {
	snap, err := p.Usage.Fetch(ctx, token)
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

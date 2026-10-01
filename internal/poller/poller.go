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

	// Probe 系列:usage endpoint 拿不到資料時的後備來源(一年期 token + 讀 response header)。
	// ProbeToken 是某一個帳號的 token,所以只套用在 ProbeAccount 那個帳號上。
	Probe        UsageFetcher
	ProbeToken   string
	ProbeAccount string

	mu        sync.Mutex
	gate      map[string]int64 // accountID -> 在此 unix 時間前不再嘗試 refresh
	backoff   map[string]int64 // accountID -> 目前退避秒數
	probeNote map[string]int64 // accountID -> 上次印「改用 probe」的時間,節流用
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
// credsRefreshAhead:CLI-backed 帳號剩餘壽命低於此(秒)就自行 refresh。
// token endpoint 會回 429,取 2 小時讓退避(600s 起跳倍增)能重試四次以上再到期。
// access token 壽命 8 小時,所以正常情況一天仍只換三到四次。
const credsRefreshAhead = 7200

// probeNoteEvery:改用 probe 後每輪都會走後備,原因只需偶爾印一次。
const probeNoteEvery = 6 * 3600

// cycle 先走 usage endpoint(免費、欄位最完整),失敗且該帳號有設 probe token 時改用 probe。
// 登入過期後 dashboard 仍有資料,不必為了額度數字每四週重登一次。
func (p *Poller) cycle(ctx context.Context, a store.Account) error {
	now := p.now()
	err := p.cycleEndpoint(ctx, a, now)
	if err == nil || p.Probe == nil || p.ProbeToken == "" || a.ID != p.ProbeAccount {
		return err
	}
	if perr := p.recordUsage(ctx, a, now, p.Probe, p.ProbeToken); perr != nil {
		return fmt.Errorf("%v; probe fallback: %w", err, perr)
	}
	p.mu.Lock()
	if p.probeNote == nil {
		p.probeNote = map[string]int64{}
	}
	last, seen := p.probeNote[a.ID]
	if !seen || now-last >= probeNoteEvery {
		p.probeNote[a.ID] = now
		log.Printf("account %s: usage endpoint unavailable, reading limits from probe headers instead: %v", a.ID, err)
	}
	p.mu.Unlock()
	return nil
}

// cycleEndpoint 用帳號自己的登入 token 打 usage endpoint。
func (p *Poller) cycleEndpoint(ctx context.Context, a store.Account, now int64) error {
	// CLI-backed 帳號:token 存在本機 claude creds 檔,refresh 後寫回同一個檔。
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
	return p.recordUsage(ctx, a, now, p.Usage, a.AccessToken)
}

// cycleCLIBacked 處理 CLI-backed 帳號:讀本機 creds、快到期時自行 refresh 並寫回檔案、拉 usage。
// 不再靠 `claude doctor` 的副作用刷 token,新版 CLI 的 doctor 已不做 OAuth refresh。
func (p *Poller) cycleCLIBacked(ctx context.Context, a store.Account, now int64) error {
	token, refresh, exp, err := readCredsToken(a.CredsPath)
	if err != nil {
		return fmt.Errorf("account %s: read creds %s: %w", a.ID, a.CredsPath, err)
	}
	// 兩顆都沒有 = CLI 已登出,再打什麼都沒用,直接回報等人重登。
	if token == "" && refresh == "" {
		return fmt.Errorf("account %s: creds %s logged out, run: claude auth login", a.ID, a.CredsPath)
	}
	if exp-now < credsRefreshAhead && refresh != "" && p.OAuth != nil {
		if !p.refreshAllowed(a.ID, now) {
			log.Printf("account %s: refresh backing off, skip this cycle", a.ID)
		} else if tok, err := p.OAuth.Refresh(ctx, refresh); err != nil {
			d := p.noteRefreshFail(a.ID, now)
			if now >= exp {
				return fmt.Errorf("account %s: token expired, refresh failed, backing off %ds: %w", a.ID, d, err)
			}
			log.Printf("account %s: refresh failed, keep token (%ds left), backing off %ds: %v", a.ID, exp-now, d, err)
		} else {
			// refresh token 用過即換,寫回失敗等於把新的那顆弄丟,下一輪會被判定重用而整串失效。
			if err := writeCredsToken(a.CredsPath, tok, now); err != nil {
				return fmt.Errorf("account %s: rotated token but writing %s failed, re-login needed: %w", a.ID, a.CredsPath, err)
			}
			p.noteRefreshOK(a.ID)
			token = tok.AccessToken
		}
	}
	if token == "" {
		return fmt.Errorf("account %s: creds %s has no access token", a.ID, a.CredsPath)
	}
	// 過期又換不到新的就別打了:拿過期 token 每輪去撞只會換來 401,還會累積成 429 限流。
	if exp > 0 && now >= exp {
		return fmt.Errorf("account %s: access token expired and not refreshed, run: claude auth login", a.ID)
	}
	return p.recordUsage(ctx, a, now, p.Usage, token)
}

// recordUsage 用指定來源與 token 拉 usage、寫 reading、偵測重置。endpoint 與 probe 共用。
func (p *Poller) recordUsage(ctx context.Context, a store.Account, now int64, src UsageFetcher, token string) error {
	snap, err := src.Fetch(ctx, token)
	if err != nil {
		return err
	}

	prev, hadPrev, err := p.Store.LatestReading(a.ID)
	if err != nil {
		return err
	}
	// probe 的 header 不帶模型名,沿用 usage endpoint 先前給過的,dashboard 標籤才不會變空白。
	if snap.ScopedLabel == "" && snap.ScopedResetsAt > 0 && hadPrev {
		snap.ScopedLabel = prev.ScopedLabel
	}

	if err := p.Store.InsertReading(store.Reading{
		AccountID: a.ID, TS: now,
		SevenDay: snap.SevenDay, FiveHour: snap.FiveHour, Sonnet: snap.Sonnet, Opus: snap.Opus,
		SevenDayResetsAt: snap.SevenDayResetsAt, FiveHourResetsAt: snap.FiveHourResetsAt,
		ScopedPct: snap.ScopedPct, ScopedLabel: snap.ScopedLabel, ScopedResetsAt: snap.ScopedResetsAt,
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

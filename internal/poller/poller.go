package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
)

type UsageFetcher interface {
	Fetch(ctx context.Context, accessToken string) (usage.Snapshot, error)
}

type Poller struct {
	Store         *store.Store
	Usage         UsageFetcher
	OAuth         *oauth.Client // may be nil in tests that never refresh
	DropPct       float64       // default 5
	RefreshBuffer int64         // seconds before expiry to refresh; default 3600
	MinAdvanceSec int64         // minimum resets_at advance to count as a natural reset; default 3600
	Now           func() int64  // default time.Now().Unix
	OnReset       func(account string, from, to float64)
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
	// refresh if near expiry; non-fatal if token still valid
	if p.OAuth != nil && a.ExpiresAt-now < buf {
		tok, err := p.OAuth.Refresh(ctx, a.RefreshToken)
		if err != nil {
			if now >= a.ExpiresAt {
				return fmt.Errorf("token expired and refresh failed: %w", err)
			}
			log.Printf("account %s: refresh failed, keep current token (%ds left): %v", a.ID, a.ExpiresAt-now, err)
		} else {
			a.AccessToken = tok.AccessToken
			a.RefreshToken = tok.RefreshToken
			a.ExpiresAt = now + tok.ExpiresIn
			if err := p.Store.UpsertAccount(a); err != nil {
				return err
			}
		}
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

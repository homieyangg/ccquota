package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	Endpoint   = "https://api.anthropic.com/api/oauth/usage"
	BetaHeader = "oauth-2025-04-20"
	UserAgent  = "claude-code/2.1.177" // WAF rejects non claude-code UAs with 429
)

type window struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// limit 對應回應裡 limits[] 的一筆。kind 目前見過 session / weekly_all / weekly_scoped,
// weekly_scoped 帶 scope.model,是實際會先卡住使用者的那條。
type limit struct {
	Kind     string  `json:"kind"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at"`
	IsActive bool    `json:"is_active"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type raw struct {
	FiveHour       *window `json:"five_hour"`
	SevenDay       *window `json:"seven_day"`
	SevenDaySonnet *window `json:"seven_day_sonnet"`
	SevenDayOpus   *window `json:"seven_day_opus"`
	Limits         []limit `json:"limits"`
}

type Snapshot struct {
	SevenDay, FiveHour, Sonnet, Opus   float64
	SevenDayResetsAt, FiveHourResetsAt int64 // unix seconds, 0 if absent

	// Scoped:限定模型的週限(limits[] 的 weekly_scoped)。API 已不再回
	// seven_day_sonnet / seven_day_opus,模型別的額度都走這裡。
	ScopedPct      float64
	ScopedLabel    string // 模型顯示名,例如 Fable
	ScopedResetsAt int64
}

type Client struct {
	HTTP *http.Client
	URL  string // defaults to Endpoint
}

func epoch(iso string) int64 {
	if iso == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func (c *Client) Fetch(ctx context.Context, accessToken string) (Snapshot, error) {
	url := c.URL
	if url == "" {
		url = Endpoint
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", BetaHeader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return Snapshot{}, fmt.Errorf("usage %d: %s", resp.StatusCode, string(body))
	}
	return Parse(body)
}

// Parse 將 /api/oauth/usage 的回應 JSON 轉成 Snapshot,由 Fetch 拉取後呼叫。
func Parse(body []byte) (Snapshot, error) {
	var r raw
	if err := json.Unmarshal(body, &r); err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if r.SevenDay != nil {
		s.SevenDay = r.SevenDay.Utilization
		s.SevenDayResetsAt = epoch(r.SevenDay.ResetsAt)
	}
	if r.FiveHour != nil {
		s.FiveHour = r.FiveHour.Utilization
		s.FiveHourResetsAt = epoch(r.FiveHour.ResetsAt)
	}
	if r.SevenDaySonnet != nil {
		s.Sonnet = r.SevenDaySonnet.Utilization
	}
	if r.SevenDayOpus != nil {
		s.Opus = r.SevenDayOpus.Utilization
	}
	applyLimits(&s, r.Limits)
	return s, nil
}

// applyLimits 用 limits[] 覆寫頂層欄位。新版回應把額度都搬進這個陣列,
// 頂層的 five_hour / seven_day 仍在但 seven_day_sonnet / opus 已固定為 null。
// 同 kind 有多筆時取百分比最高的,寧可高估也不要漏報。
func applyLimits(s *Snapshot, limits []limit) {
	for _, l := range limits {
		switch l.Kind {
		case "session":
			if l.Percent >= s.FiveHour {
				s.FiveHour = l.Percent
				if ts := epoch(l.ResetsAt); ts != 0 {
					s.FiveHourResetsAt = ts
				}
			}
		case "weekly_all":
			if l.Percent >= s.SevenDay {
				s.SevenDay = l.Percent
				if ts := epoch(l.ResetsAt); ts != 0 {
					s.SevenDayResetsAt = ts
				}
			}
		case "weekly_scoped":
			if l.Percent >= s.ScopedPct {
				s.ScopedPct = l.Percent
				s.ScopedResetsAt = epoch(l.ResetsAt)
				s.ScopedLabel = scopeLabel(l)
			}
		}
	}
}

// scopeLabel 取 weekly_scoped 的模型顯示名,取不到就回空字串由呼叫端決定怎麼顯示。
func scopeLabel(l limit) string {
	if l.Scope != nil && l.Scope.Model != nil {
		return l.Scope.Model.DisplayName
	}
	return ""
}

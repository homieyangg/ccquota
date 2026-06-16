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

type raw struct {
	FiveHour       *window `json:"five_hour"`
	SevenDay       *window `json:"seven_day"`
	SevenDaySonnet *window `json:"seven_day_sonnet"`
	SevenDayOpus   *window `json:"seven_day_opus"`
}

type Snapshot struct {
	SevenDay, FiveHour, Sonnet, Opus   float64
	SevenDayResetsAt, FiveHourResetsAt int64 // unix seconds, 0 if absent
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

// Parse 將 /api/oauth/usage 的回應 JSON 轉成 Snapshot。
// 供 Fetch（自行拉取）與外部推送端點（POST /v1/usage）共用。
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
	return s, nil
}

package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

const (
	ProbeEndpoint = "https://api.anthropic.com/v1/messages"
	// ProbeModel 預設打有模型別週限的那個模型:7d_oi 這組 header 只有打到它才會回。
	ProbeModel     = "claude-fable-5-1"
	probeUserAgent = "claude-cli/2.1.259 (external, cli)"
	// 訂閱 OAuth token 打 /v1/messages 需要這段 system 開頭,少了會被當成非 Claude Code 流量拒絕。
	probeSystem = "You are Claude Code, Anthropic's official CLI for Claude."

	headerPrefix = "anthropic-ratelimit-unified-"
)

// ProbeClient 發一個 max_tokens=1 的推論請求,從 response header 讀額度。
// 給 `claude setup-token` 的一年期 token 用:那種 token 只有推論權限,
// 打 /api/oauth/usage 會 403,但每個推論回應的 header 都帶著同一組額度數字。
type ProbeClient struct {
	HTTP  *http.Client
	URL   string // defaults to ProbeEndpoint
	Model string // defaults to ProbeModel
}

func (c *ProbeClient) Fetch(ctx context.Context, accessToken string) (Snapshot, error) {
	url, model := c.URL, c.Model
	if url == "" {
		url = ProbeEndpoint
	}
	if model == "" {
		model = ProbeModel
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"system":     []map[string]string{{"type": "text", "text": probeSystem}},
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", BetaHeader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", probeUserAgent)

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	// 額度用完時請求本身會回 429,但 header 照樣帶著數字,那正是最需要顯示的時候,
	// 所以只要 header 在就採用,不看 status。
	if s, ok := ParseHeaders(resp.Header); ok {
		return s, nil
	}
	return Snapshot{}, fmt.Errorf("probe %d without rate-limit headers: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

// ParseHeaders 把 anthropic-ratelimit-unified-* 轉成 Snapshot。utilization 是 0~1 的比例,
// 轉成跟 usage endpoint 一致的百分比。5h 與 7d 都沒有時回 ok=false。
// ScopedLabel 留空:header 不帶模型名,由呼叫端沿用先前已知的名稱。
func ParseHeaders(h http.Header) (Snapshot, bool) {
	var s Snapshot
	fh, okFH := headerPct(h, "5h-utilization")
	sd, okSD := headerPct(h, "7d-utilization")
	if !okFH && !okSD {
		return Snapshot{}, false
	}
	s.FiveHour, s.SevenDay = fh, sd
	s.FiveHourResetsAt = headerInt(h, "5h-reset")
	s.SevenDayResetsAt = headerInt(h, "7d-reset")
	if sc, ok := headerPct(h, "7d_oi-utilization"); ok {
		s.ScopedPct = sc
		s.ScopedResetsAt = headerInt(h, "7d_oi-reset")
	}
	return s, true
}

func headerPct(h http.Header, name string) (float64, bool) {
	v := h.Get(headerPrefix + name)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return math.Round(f*1000) / 10, true
}

func headerInt(h http.Header, name string) int64 {
	n, _ := strconv.ParseInt(h.Get(headerPrefix+name), 10, 64)
	return n
}

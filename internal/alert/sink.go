package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Sink 是通知後端介面。Send 發送純文字（或 HTML）訊息；
// Lang 回傳此頻道偏好的通知語言（空字串代表沿用全域）。
type Sink interface {
	Send(ctx context.Context, text string) error
	Lang() string
}

// post 送出 POST 請求並要求 2xx 狀態碼，client 為 nil 時用 http.DefaultClient。
// label 用於組裝錯誤訊息（如 "telegram"、"webhook"）。
func post(ctx context.Context, client *http.Client, label, urlStr, contentType string, body io.Reader) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", label, resp.StatusCode)
	}
	return nil
}

// TelegramSink 透過 Telegram Bot API 發送訊息。
// 若 HTTP 為 nil 則使用 http.DefaultClient。
// BaseURL 供測試覆寫；預設為 "https://api.telegram.org"。
type TelegramSink struct {
	Token    string
	ChatID   string
	Language string       // 通知語言；空 = 沿用全域
	BaseURL  string       // optional; default "https://api.telegram.org"
	HTTP     *http.Client // optional; default http.DefaultClient
}

func (t *TelegramSink) Lang() string { return t.Language }

func (t *TelegramSink) Send(ctx context.Context, text string) error {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	apiURL := fmt.Sprintf("%s/bot%s/sendMessage", base, t.Token)

	body := url.Values{}
	body.Set("chat_id", t.ChatID)
	body.Set("text", text)
	body.Set("parse_mode", "HTML")

	return post(ctx, t.HTTP, "telegram", apiURL, "application/x-www-form-urlencoded",
		strings.NewReader(body.Encode()))
}

// WebhookSink 透過 HTTP POST JSON {"text":"..."} 發送訊息。
// 若 HTTP 為 nil 則使用 http.DefaultClient。
type WebhookSink struct {
	URL      string
	Language string       // 通知語言；空 = 沿用全域
	HTTP     *http.Client // optional; default http.DefaultClient
}

func (w *WebhookSink) Lang() string { return w.Language }

func (w *WebhookSink) Send(ctx context.Context, text string) error {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	return post(ctx, w.HTTP, "webhook", w.URL, "application/json", bytes.NewReader(payload))
}

// BuildSink 依頻道型別與(已解密的)設定建出對應的 Sink。
func BuildSink(chType string, cfg map[string]string) (Sink, error) {
	switch chType {
	case "telegram":
		return &TelegramSink{Token: cfg["bot_token"], ChatID: cfg["chat_id"], Language: cfg["lang"]}, nil
	case "webhook":
		return &WebhookSink{URL: cfg["url"], Language: cfg["lang"]}, nil
	default:
		return nil, fmt.Errorf("alert: unknown channel type %q", chType)
	}
}

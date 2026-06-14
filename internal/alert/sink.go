package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Sink 是通知後端介面。Send 發送純文字（或 HTML）訊息。
type Sink interface {
	Send(ctx context.Context, text string) error
}

// TelegramSink 透過 Telegram Bot API 發送訊息。
// 若 HTTP 為 nil 則使用 http.DefaultClient。
// BaseURL 供測試覆寫；預設為 "https://api.telegram.org"。
type TelegramSink struct {
	Token   string
	ChatID  string
	BaseURL string       // optional; default "https://api.telegram.org"
	HTTP    *http.Client // optional; default http.DefaultClient
}

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

	client := t.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram: HTTP %d", resp.StatusCode)
	}
	return nil
}

// WebhookSink 透過 HTTP POST JSON {"text":"..."} 發送訊息。
// 若 HTTP 為 nil 則使用 http.DefaultClient。
type WebhookSink struct {
	URL  string
	HTTP *http.Client // optional; default http.DefaultClient
}

func (w *WebhookSink) Send(ctx context.Context, text string) error {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	client := w.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL,
		bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}

// BuildSink 依頻道型別與(已解密的)設定建出對應的 Sink。
func BuildSink(chType string, cfg map[string]string) (Sink, error) {
	switch chType {
	case "telegram":
		return &TelegramSink{Token: cfg["bot_token"], ChatID: cfg["chat_id"]}, nil
	case "webhook":
		return &WebhookSink{URL: cfg["url"]}, nil
	default:
		return nil, fmt.Errorf("alert: unknown channel type %q", chType)
	}
}

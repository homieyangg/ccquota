package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Sink 是通知後端介面。
//   - Send 發送訊息,回傳可供日後編輯的 ref(Telegram 為 message_id;不支援者回 "")。
//   - Edit 就地改寫先前 Send 出去的訊息(warn 升 crit 用);不支援者回 ErrEditUnsupported。
//   - Lang 回傳此頻道偏好語言(空 = 沿用全域)。
//   - Key 回傳此 sink 的穩定識別,供 per-sink 記錄已送訊息。
type Sink interface {
	Send(ctx context.Context, text string) (ref string, err error)
	Edit(ctx context.Context, ref, text string) error
	Lang() string
	Key() string
}

// ErrEditUnsupported 表示此 sink 不支援就地編輯,呼叫端應退化成重送。
var ErrEditUnsupported = fmt.Errorf("alert: edit unsupported by sink")

// post 送出 POST 請求並要求 2xx,回傳 response body。client 為 nil 時用 http.DefaultClient。
// label 用於組裝錯誤訊息(如 "telegram"、"webhook")。
func post(ctx context.Context, client *http.Client, label, urlStr, contentType string, body io.Reader) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return respBody, fmt.Errorf("%s: HTTP %d", label, resp.StatusCode)
	}
	return respBody, nil
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

func (t *TelegramSink) Key() string { return "tg:" + t.ChatID }

func (t *TelegramSink) base() string {
	if t.BaseURL != "" {
		return t.BaseURL
	}
	return "https://api.telegram.org"
}

// Send 發訊息並從回應解析 message_id 當 ref(供日後 editMessageText)。
func (t *TelegramSink) Send(ctx context.Context, text string) (string, error) {
	apiURL := fmt.Sprintf("%s/bot%s/sendMessage", t.base(), t.Token)
	body := url.Values{}
	body.Set("chat_id", t.ChatID)
	body.Set("text", text)
	body.Set("parse_mode", "HTML")
	respBody, err := post(ctx, t.HTTP, "telegram", apiURL, "application/x-www-form-urlencoded",
		strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &r); err != nil || r.Result.MessageID == 0 {
		return "", nil // 送出成功但解析不到 id;退而求其次不給 ref(日後升級會重送)
	}
	return strconv.FormatInt(r.Result.MessageID, 10), nil
}

// Edit 對既有 message_id 就地改寫內容(warn 升 crit)。
func (t *TelegramSink) Edit(ctx context.Context, ref, text string) error {
	apiURL := fmt.Sprintf("%s/bot%s/editMessageText", t.base(), t.Token)
	body := url.Values{}
	body.Set("chat_id", t.ChatID)
	body.Set("message_id", ref)
	body.Set("text", text)
	body.Set("parse_mode", "HTML")
	_, err := post(ctx, t.HTTP, "telegram", apiURL, "application/x-www-form-urlencoded",
		strings.NewReader(body.Encode()))
	return err
}

// WebhookSink 透過 HTTP POST JSON {"text":"..."} 發送訊息。
// 若 HTTP 為 nil 則使用 http.DefaultClient。
type WebhookSink struct {
	URL      string
	Language string       // 通知語言；空 = 沿用全域
	HTTP     *http.Client // optional; default http.DefaultClient
}

func (w *WebhookSink) Lang() string { return w.Language }

func (w *WebhookSink) Key() string { return "wh:" + w.URL }

func (w *WebhookSink) Send(ctx context.Context, text string) (string, error) {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	_, err = post(ctx, w.HTTP, "webhook", w.URL, "application/json", bytes.NewReader(payload))
	return "", err
}

// Edit:webhook 無法就地編輯,回 ErrEditUnsupported 讓呼叫端退化成重送。
func (w *WebhookSink) Edit(_ context.Context, _, _ string) error {
	return ErrEditUnsupported
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

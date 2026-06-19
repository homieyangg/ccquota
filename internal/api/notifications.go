package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/ccquota/ccquota/internal/alert"
	"github.com/ccquota/ccquota/internal/store"
)

// telegram 的 secret 欄位集合(其餘型別之後擴充)。
var secretFields = map[string][]string{
	"telegram": {"bot_token"},
	"webhook":  {},
}

// secretSet 回傳該頻道型別的 secret 欄位集合,供遮罩與加密共用。
func secretSet(chType string) map[string]bool {
	set := make(map[string]bool, len(secretFields[chType]))
	for _, f := range secretFields[chType] {
		set[f] = true
	}
	return set
}

// maskSecret 把 secret 遮成 ••••<last4>;短於 4 碼全遮。
func maskSecret(plain string) string {
	if plain == "" {
		return ""
	}
	if len(plain) <= 4 {
		return "••••"
	}
	return "••••" + plain[len(plain)-4:]
}

// decryptConfig 解密 config 內每個欄位(非 enc: 前綴者原樣回傳)。
func (h *handler) decryptConfig(raw map[string]string) map[string]string {
	dec := make(map[string]string, len(raw))
	for k, v := range raw {
		pt, err := h.cipher.Decrypt(v)
		if err != nil {
			log.Printf("notifications: decrypt field %s: %v", k, err)
			pt = ""
		}
		dec[k] = pt
	}
	return dec
}

// parseChannelConfig 解析頻道 config JSON；格式錯誤時記 log 並回 nil(視同空設定)。
func parseChannelConfig(config string, chID int64) map[string]string {
	var raw map[string]string
	if err := json.Unmarshal([]byte(config), &raw); err != nil {
		log.Printf("notifications: channel %d bad config json: %v", chID, err)
	}
	return raw
}

// channelConfigMasked 解析、解密並遮蔽某頻道的 config，供 GET 顯示用。
func (h *handler) channelConfigMasked(ch store.Channel) map[string]string {
	dec := h.decryptConfig(parseChannelConfig(ch.Config, ch.ID))
	secrets := secretSet(ch.Type)
	masked := make(map[string]string, len(dec))
	for k, v := range dec {
		if secrets[k] {
			masked[k] = maskSecret(v)
		} else {
			masked[k] = v
		}
	}
	return masked
}

type channelView struct {
	ID      int64             `json:"id"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"` // secret 已遮蔽
}

// handleNotifications: GET 回頻道(遮罩)+ 門檻。
func (h *handler) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chs, err := h.s.ListChannels()
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	views := make([]channelView, 0, len(chs))
	for _, ch := range chs {
		views = append(views, channelView{ID: ch.ID, Type: ch.Type, Enabled: ch.Enabled, Config: h.channelConfigMasked(ch)})
	}
	th, _ := h.s.GetAlertThresholds()
	writeJSON(w, map[string]any{"channels": views, "thresholds": th})
}

type channelReq struct {
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// encryptConfig 把 secret 欄位加密;沒重送(空字串)的 secret 保留舊值 prev。
func (h *handler) encryptConfig(chType string, in map[string]string, prev map[string]string) (string, error) {
	secrets := secretSet(chType)
	out := make(map[string]string, len(in))
	for k, v := range in {
		if secrets[k] {
			if v == "" && prev != nil {
				out[k] = prev[k]
				continue
			}
			enc, err := h.cipher.Encrypt(v)
			if err != nil {
				return "", err
			}
			out[k] = enc
		} else {
			out[k] = v
		}
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// handleChannelsCollection: POST 新增頻道。
func (h *handler) handleChannelsCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req channelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "bad json", http.StatusBadRequest)
		return
	}
	if _, ok := secretFields[req.Type]; !ok {
		jsonError(w, "unknown channel type", http.StatusBadRequest)
		return
	}
	cfg, err := h.encryptConfig(req.Type, req.Config, nil)
	if err != nil {
		jsonError(w, "encrypt error", http.StatusInternalServerError)
		return
	}
	id, err := h.s.CreateChannel(store.Channel{Type: req.Type, Config: cfg, Enabled: req.Enabled})
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id})
}

// handleChannelItem: PUT 更新 / DELETE 刪除 / POST .../test 測試。
func (h *handler) handleChannelItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/notifications/channels/")
	test := false
	if strings.HasSuffix(rest, "/test") {
		test = true
		rest = strings.TrimSuffix(rest, "/test")
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cur, ok, err := h.s.GetChannel(id)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch {
	case test && r.Method == http.MethodPost:
		h.testChannel(w, r, cur)
	case r.Method == http.MethodPut:
		var req channelReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "bad json", http.StatusBadRequest)
			return
		}
		prev := parseChannelConfig(cur.Config, id)
		cfg, err := h.encryptConfig(cur.Type, req.Config, prev)
		if err != nil {
			jsonError(w, "encrypt error", http.StatusInternalServerError)
			return
		}
		if err := h.s.UpdateChannel(store.Channel{ID: id, Type: cur.Type, Config: cfg, Enabled: req.Enabled}); err != nil {
			jsonError(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	case r.Method == http.MethodDelete:
		if err := h.s.DeleteChannel(id); err != nil {
			jsonError(w, "db error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// testChannel 用該頻道送一則測試訊息。
func (h *handler) testChannel(w http.ResponseWriter, r *http.Request, ch store.Channel) {
	dec := h.decryptConfig(parseChannelConfig(ch.Config, ch.ID))
	sink, err := alert.BuildSink(ch.Type, dec)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := sink.Send(r.Context(), "ccquota test message ✅"); err != nil {
		// 不可把原始 err 回給 client:transport 失敗時 *url.Error 會帶出
		// 含 bot token 的完整 URL(…/bot<TOKEN>/sendMessage),只在 server 端記錄。
		log.Printf("notifications: channel %d test send: %v", ch.ID, err)
		writeJSON(w, map[string]string{"status": "error", "error": "send failed"})
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleThresholds: PUT 更新門檻。
func (h *handler) handleThresholds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var t store.AlertThresholds
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		jsonError(w, "bad json", http.StatusBadRequest)
		return
	}
	// 平分額度門檻:warn 必須小於 crit(否則 warn 永遠不會觸發)。負值視為未設(走預設)。
	if t.UserShareWarn < 0 {
		t.UserShareWarn = 0
	}
	if t.UserShareCrit < 0 {
		t.UserShareCrit = 0
	}
	if t.UserShareWarn > 0 && t.UserShareCrit > 0 && t.UserShareWarn >= t.UserShareCrit {
		jsonError(w, "user_share_warn must be less than user_share_crit", http.StatusBadRequest)
		return
	}
	if err := h.s.SetAlertThresholds(t); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

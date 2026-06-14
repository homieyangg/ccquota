package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ccquota/ccquota/internal/update"
)

// updateRepo 是檢查更新的 GitHub repo,可由 CCQUOTA_UPDATE_REPO 覆寫。
func updateRepo() string {
	if v := os.Getenv("CCQUOTA_UPDATE_REPO"); v != "" {
		return v
	}
	return "homieyangg/ccquota"
}

// latestRelease 取最新 release,結果快取 30 分鐘;force=true 時略過快取。
func (h *handler) latestRelease(ctx context.Context, force bool) (update.Release, error) {
	h.updMu.Lock()
	defer h.updMu.Unlock()
	if !force && h.updRel.Tag != "" && time.Since(h.updAt) < 30*time.Minute {
		return h.updRel, nil
	}
	rel, err := update.Latest(ctx, updateRepo())
	if err != nil {
		return update.Release{}, err
	}
	h.updRel = rel
	h.updAt = time.Now()
	return rel, nil
}

// handleVersion: GET /api/version?check=1
// 回 {current, latest, update_available, notes_url}。check=1 時強制重新查 GitHub。
func (h *handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := map[string]any{"current": h.version, "latest": "", "update_available": false}
	rel, err := h.latestRelease(r.Context(), r.URL.Query().Get("check") == "1")
	if err != nil {
		// 查不到最新版不是致命錯誤(可能離線/限流);照樣回目前版本
		log.Printf("version: latest lookup: %v", err)
		writeJSON(w, out)
		return
	}
	out["latest"] = rel.Tag
	out["notes_url"] = rel.NotesURL
	out["update_available"] = update.Newer(h.version, rel.Tag)
	writeJSON(w, out)
}

// handleUpdate: POST /api/update(admin)。下載最新版、驗 checksum、原子替換,然後 re-exec 重啟。
func (h *handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel, err := h.latestRelease(r.Context(), true)
	if err != nil {
		jsonError(w, "cannot reach release server", http.StatusBadGateway)
		return
	}
	if !update.Newer(h.version, rel.Tag) {
		jsonError(w, "already up to date", http.StatusBadRequest)
		return
	}
	// 下載 + 驗 checksum + 原子替換(用獨立 context,不被 client 斷線取消)
	if err := update.Apply(context.Background(), rel); err != nil {
		log.Printf("update apply: %v", err)
		jsonError(w, "update failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "updating", "version": rel.Tag})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	log.Printf("update: applied %s, re-exec in 500ms", rel.Tag)
	// 稍候讓 response 送出,再 re-exec 換成新 binary(同 PID)
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := update.Restart(); err != nil {
			log.Printf("update: restart failed: %v", err)
		}
	}()
}

// writeJSON 寫 JSON 回應。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

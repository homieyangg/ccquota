package api

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/ccquota/ccquota/internal/alert"
	"github.com/ccquota/ccquota/internal/calc"
	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/secret"
	"github.com/ccquota/ccquota/internal/store"
)

const (
	pbkdf2Iter   = 100000
	pbkdf2KeyLen = 32
	saltLen      = 16
	settingHash  = "admin_password_hash"
	settingSalt  = "admin_password_salt"
	settingMust  = "admin_must_change"
)

// AdminPassword 是啟動時使用的明文密碼（僅用於 bootstrap）。
// 若 CCQUOTA_ADMIN_PASSWORD 已設定則為其值；否則為自動產生值（會被 log）。
var AdminPassword string

// SeededAutoPassword 只有「這次啟動真的自動產生並存了密碼」時為 true。
// 重啟時 store 已有 hash 就不會再 seed，也就不該再 log 那組沒用到的密碼。
var SeededAutoPassword bool

func init() {
	AdminPassword = os.Getenv("CCQUOTA_ADMIN_PASSWORD")
	if AdminPassword == "" {
		b := make([]byte, 16)
		rand.Read(b)
		AdminPassword = hex.EncodeToString(b)
	}
}

// hashPassword 使用 PBKDF2-SHA256 對密碼做 hash，回傳 hexHash 和 hexSalt。
func hashPassword(password string) (hexHash, hexSalt string, err error) {
	salt := make([]byte, saltLen)
	if _, err = rand.Read(salt); err != nil {
		return "", "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, pbkdf2KeyLen)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(key), hex.EncodeToString(salt), nil
}

// verifyPassword 以 constant-time 驗證密碼是否符合儲存的 hash。
func verifyPassword(password, hexHash, hexSalt string) bool {
	salt, err := hex.DecodeString(hexSalt)
	if err != nil {
		return false
	}
	expectedKey, err := hex.DecodeString(hexHash)
	if err != nil {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, pbkdf2KeyLen)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, expectedKey) == 1
}

// bootstrapPassword 在 store 中尚無 hash 時，依 env 設定做初始化。
// 若 CCQUOTA_ADMIN_PASSWORD 有設定 → 存 hash，must_change=0。
// 否則（自動產生）→ 存 hash，must_change=1。
func bootstrapPassword(s *store.Store) error {
	_, ok, err := s.GetSetting(settingHash)
	if err != nil {
		return err
	}
	if ok {
		// 已有 hash，不覆蓋
		return nil
	}
	mustChange := "0"
	if os.Getenv("CCQUOTA_ADMIN_PASSWORD") == "" {
		mustChange = "1"
		SeededAutoPassword = true
	}
	h, salt, err := hashPassword(AdminPassword)
	if err != nil {
		return err
	}
	if err := s.SetSetting(settingHash, h); err != nil {
		return err
	}
	if err := s.SetSetting(settingSalt, salt); err != nil {
		return err
	}
	return s.SetSetting(settingMust, mustChange)
}

// checkStoredPassword 從 store 讀出 hash/salt 並驗證密碼。
func checkStoredPassword(s *store.Store, password string) bool {
	h, ok, err := s.GetSetting(settingHash)
	if err != nil || !ok {
		return false
	}
	salt, ok, err := s.GetSetting(settingSalt)
	if err != nil || !ok {
		return false
	}
	return verifyPassword(password, h, salt)
}

// mustChangeFlag 讀取 admin_must_change 設定；預設 false。
func mustChangeFlag(s *store.Store) bool {
	v, ok, err := s.GetSetting(settingMust)
	if err != nil || !ok {
		return false
	}
	return v == "1"
}

type pendingLogin struct {
	verifier  string
	state     string
	createdAt time.Time
}

type handler struct {
	s           *store.Store
	oc          *oauth.Client
	staleSec    int64
	ingestToken string
	publicURL   string
	cipher      *secret.Cipher
	mu          sync.Mutex
	pending     map[string]pendingLogin
}

// New returns an http.Handler with all API routes mounted.
// staleSec 為帳號資料視為過時的秒數閾值（對應 CCQUOTA_POLLER_STALE_SEC）。
// ingestToken 為 /v1/metrics 的 Bearer token（空字串 = 關閉 enroll 功能）。
// publicURL 為對外 URL（空字串時從 request 自動推導）。
func New(s *store.Store, oc *oauth.Client, staleSec int64, ingestToken, publicURL string, cipher *secret.Cipher) http.Handler {
	// 啟動時 bootstrap 密碼 hash（若尚未存過）
	if err := bootstrapPassword(s); err != nil {
		// 非致命，但要 log
		fmt.Fprintf(os.Stderr, "ccquota: bootstrap password: %v\n", err)
	}

	h := &handler{
		s:           s,
		oc:          oc,
		staleSec:    staleSec,
		ingestToken: ingestToken,
		publicURL:   strings.TrimRight(publicURL, "/"),
		cipher:      cipher,
		pending:     make(map[string]pendingLogin),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	// 管理員認證端點（不需要 gate）
	mux.HandleFunc("/api/auth/login", h.handleAuthLogin)
	mux.HandleFunc("/api/auth/logout", h.handleAuthLogout)
	mux.HandleFunc("/api/auth/status", h.handleAuthStatus)
	// 以下路由需要 admin 認證（session cookie 或 Basic Auth）
	mux.Handle("/api/accounts", adminAuth(s, http.HandlerFunc(h.handleAccounts)))
	mux.Handle("/api/history", adminAuth(s, http.HandlerFunc(h.handleHistory)))
	mux.Handle("/api/login/start", adminAuth(s, http.HandlerFunc(h.handleLoginStart)))
	mux.Handle("/api/login/complete", adminAuth(s, http.HandlerFunc(h.handleLoginComplete)))
	mux.Handle("/api/enroll", adminAuth(s, http.HandlerFunc(h.handleEnroll)))
	mux.Handle("/api/auth/change-password", adminAuth(s, http.HandlerFunc(h.handleChangePassword)))
	// /e/<token>: enrollment script，不需要 admin auth
	mux.HandleFunc("/e/", h.handleEnrollScript)
	// 通知設定端點
	mux.Handle("/api/notifications", adminAuth(s, http.HandlerFunc(h.handleNotifications)))
	mux.Handle("/api/notifications/channels", adminAuth(s, http.HandlerFunc(h.handleChannelsCollection)))
	mux.Handle("/api/notifications/channels/", adminAuth(s, http.HandlerFunc(h.handleChannelItem)))
	mux.Handle("/api/notifications/thresholds", adminAuth(s, http.HandlerFunc(h.handleThresholds)))
	return mux
}

// baseURL 從 request 推導對外 base URL（scheme://host）。
// 若 h.publicURL 非空則直接回傳。
func (h *handler) baseURL(r *http.Request) string {
	if h.publicURL != "" {
		return h.publicURL
	}
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else {
		host := r.Host
		if strings.HasPrefix(host, "localhost") ||
			strings.HasPrefix(host, "127.") ||
			strings.HasPrefix(host, "[::1]") {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

// adminAuth 是管理員認證 middleware，接受 session cookie 或 Basic Auth 其中之一。
// 密碼驗證優先使用 store 中的 hash；hash 不存在時退回比對 AdminPassword（容錯）。
func adminAuth(s *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasValidSession(r) {
			next.ServeHTTP(w, r)
			return
		}
		_, pass, ok := r.BasicAuth()
		if ok && checkStoredPassword(s, pass) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="ccquota"`)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
	})
}

// authLoginReq 是 POST /api/auth/login 的請求結構。
type authLoginReq struct {
	Password string `json:"password"`
}

// handleAuthLogin 處理管理員登入，成功後設置 session cookie。
func (h *handler) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req authLoginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !checkStoredPassword(h.s, req.Password) {
		jsonError(w, "invalid password", http.StatusUnauthorized)
		return
	}
	token, err := globalSessions.create()
	if err != nil {
		jsonError(w, "session error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, r, token)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAuthLogout 清除 session cookie 並刪除 session。
func (h *handler) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		globalSessions.delete(c.Value)
	}
	clearSessionCookie(w, r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleAuthStatus 回傳目前是否已認證，以及是否需要強制改密碼。
func (h *handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	authed := false
	if hasValidSession(r) {
		authed = true
	} else if _, pass, ok := r.BasicAuth(); ok {
		if checkStoredPassword(h.s, pass) {
			authed = true
		}
	}
	mustChange := false
	if authed {
		mustChange = mustChangeFlag(h.s)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"authed":      authed,
		"must_change": mustChange,
	})
}

// changePasswordReq 是 POST /api/auth/change-password 的請求結構。
type changePasswordReq struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// handleChangePassword 讓已認證的 admin 變更密碼。
func (h *handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req changePasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	// 驗證現有密碼
	if !checkStoredPassword(h.s, req.Current) {
		jsonError(w, "current password is incorrect", http.StatusUnauthorized)
		return
	}
	// 驗證新密碼規則：長度 >= 8，且不能與舊密碼相同
	if len(req.New) < 8 {
		jsonError(w, "new password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if req.New == req.Current {
		jsonError(w, "new password must differ from current password", http.StatusBadRequest)
		return
	}
	// 產生新的 hash 並儲存
	h2, salt, err := hashPassword(req.New)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.s.SetSetting(settingHash, h2); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if err := h.s.SetSetting(settingSalt, salt); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if err := h.s.SetSetting(settingMust, "0"); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// userCostResp 代表單一使用者的成本明細。
type userCostResp struct {
	User     string  `json:"user"`
	CostUSD  float64 `json:"cost_usd"`
	Tokens   int64   `json:"tokens"`
	SharePct float64 `json:"share_pct"`
}

// costResp 代表帳號期間成本與反推週額度資訊。
type costResp struct {
	PeriodCostUSD    float64        `json:"period_cost_usd"`
	WeeklyBudgetUSD  float64        `json:"weekly_budget_usd"`
	PerUserBudgetUSD float64        `json:"per_user_budget_usd"`
	Users            []userCostResp `json:"users"`
}

type accountResp struct {
	ID               string   `json:"id"`
	Label            string   `json:"label"`
	SevenDay         *float64 `json:"seven_day"`
	FiveHour         *float64 `json:"five_hour"`
	Sonnet           *float64 `json:"sonnet"`
	Opus             *float64 `json:"opus"`
	SevenDayResetsAt *int64   `json:"seven_day_resets_at"`
	FiveHourResetsAt *int64   `json:"five_hour_resets_at"`
	ReadingTS        *int64   `json:"reading_ts"`
	Stale            bool     `json:"stale"`
	HasReading       bool     `json:"has_reading"`
	Cost             costResp `json:"cost"`
}

func (h *handler) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	accts, err := h.s.ListAccounts()
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	now := time.Now().Unix()
	out := make([]accountResp, 0, len(accts))
	for _, a := range accts {
		ar := accountResp{ID: a.ID, Label: a.Label}
		reading, ok, err := h.s.LatestReading(a.ID)
		if err != nil {
			jsonError(w, "db error", http.StatusInternalServerError)
			return
		}

		// 決定期間起始時間：seven_day_resets_at - 7天；無 reading 則 now - 7天。
		var sinceTS int64
		if ok {
			ar.HasReading = true
			ar.SevenDay = &reading.SevenDay
			ar.FiveHour = &reading.FiveHour
			ar.Sonnet = &reading.Sonnet
			ar.Opus = &reading.Opus
			ar.SevenDayResetsAt = &reading.SevenDayResetsAt
			ar.FiveHourResetsAt = &reading.FiveHourResetsAt
			ar.ReadingTS = &reading.TS
			staleThresh := h.staleSec
			if staleThresh <= 0 {
				staleThresh = 1800
			}
			staleData := now-reading.TS > staleThresh
			pastReset := reading.SevenDayResetsAt > 0 && now > reading.SevenDayResetsAt
			ar.Stale = staleData || pastReset
			sinceTS = reading.SevenDayResetsAt - 7*24*3600
		} else {
			sinceTS = now - 7*24*3600
		}

		// 計算期間成本與反推週額度。
		costInfo, err := h.buildCost(a.ID, sinceTS, reading, ok)
		if err != nil {
			jsonError(w, "db error", http.StatusInternalServerError)
			return
		}
		ar.Cost = costInfo

		out = append(out, ar)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// buildCost 計算帳號的期間成本、反推週額度與每人份額。
func (h *handler) buildCost(accountID string, sinceTS int64, reading store.Reading, hasReading bool) (costResp, error) {
	const minPct = 5.0

	periodCost, err := h.s.AccountPeriodCost(accountID, sinceTS)
	if err != nil {
		return costResp{}, err
	}

	userCosts, err := h.s.UserPeriodCosts(accountID, sinceTS)
	if err != nil {
		return costResp{}, err
	}

	// 反推週額度：需要 reading 且 7d% >= minPct。
	var sevenDayPct float64
	if hasReading {
		sevenDayPct = reading.SevenDay
	}
	weeklyBudget := calc.WeeklyBudget(periodCost, sevenDayPct, minPct)

	// userCount = 有成本的不同使用者數，最少 1。
	userCount := len(userCosts)
	if userCount < 1 {
		userCount = 1
	}
	perUserBudget := calc.PerUserBudget(weeklyBudget, userCount)

	// 建立 user 清單，依 cost 降序排列。
	users := make([]userCostResp, 0, len(userCosts))
	for u, uc := range userCosts {
		users = append(users, userCostResp{
			User:     u,
			CostUSD:  uc.Cost,
			Tokens:   uc.Tokens,
			SharePct: calc.SharePct(uc.Cost, perUserBudget),
		})
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].CostUSD > users[j].CostUSD
	})

	return costResp{
		PeriodCostUSD:    periodCost,
		WeeklyBudgetUSD:  weeklyBudget,
		PerUserBudgetUSD: perUserBudget,
		Users:            users,
	}, nil
}

type historyPoint struct {
	TS       int64   `json:"ts"`
	SevenDay float64 `json:"seven_day"`
	FiveHour float64 `json:"five_hour"`
}

func (h *handler) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	acctID := r.URL.Query().Get("account")
	if acctID == "" {
		jsonError(w, "account required", http.StatusBadRequest)
		return
	}
	hoursStr := r.URL.Query().Get("hours")
	hours := int64(168)
	if hoursStr != "" {
		if n, err := strconv.ParseInt(hoursStr, 10, 64); err == nil && n > 0 {
			hours = n
		}
	}
	sinceTS := time.Now().Unix() - hours*3600
	readings, err := h.s.History(acctID, sinceTS)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	out := make([]historyPoint, 0, len(readings))
	for _, r := range readings {
		out = append(out, historyPoint{TS: r.TS, SevenDay: r.SevenDay, FiveHour: r.FiveHour})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

type loginStartReq struct {
	Label string `json:"label"`
}

func (h *handler) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req loginStartReq
	// label 為選填；body 為空或格式不符都可繼續（label 留空即可）。
	_ = json.NewDecoder(r.Body).Decode(&req)

	// expire stale pending entries
	h.mu.Lock()
	for k, v := range h.pending {
		if time.Since(v.createdAt) > 15*time.Minute {
			delete(h.pending, k)
		}
	}
	h.mu.Unlock()

	pkce, err := oauth.NewPKCE()
	if err != nil {
		jsonError(w, "pkce error", http.StatusInternalServerError)
		return
	}

	loginID := fmt.Sprintf("%x", randomBytes(16))
	h.mu.Lock()
	h.pending[loginID] = pendingLogin{verifier: pkce.Verifier, state: pkce.State, createdAt: time.Now()}
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"login_id":      loginID,
		"authorize_url": oauth.AuthorizeURL(pkce),
	})
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

type loginCompleteReq struct {
	LoginID string `json:"login_id"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	Code    string `json:"code"`
}

func (h *handler) handleLoginComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req loginCompleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.LoginID == "" || req.ID == "" || req.Code == "" {
		jsonError(w, "login_id, id, and code are required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	pend, ok := h.pending[req.LoginID]
	if ok {
		delete(h.pending, req.LoginID)
	}
	h.mu.Unlock()
	if !ok {
		jsonError(w, "login_id not found or expired", http.StatusBadRequest)
		return
	}

	pkce := oauth.PKCE{Verifier: pend.verifier, State: pend.state}
	tok, err := h.oc.ExchangeCode(r.Context(), req.Code, pkce)
	if err != nil {
		jsonError(w, fmt.Sprintf("exchange failed: %v", err), http.StatusBadRequest)
		return
	}

	lbl := req.Label
	if lbl == "" {
		lbl = req.ID
	}
	if err := h.s.UpsertAccount(store.Account{
		ID: req.ID, Label: lbl,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tok.ExpiresIn,
	}); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

type enrollReq struct {
	Account string `json:"account"`
	User    string `json:"user"`
}

func (h *handler) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.ingestToken == "" {
		jsonError(w, "ingest not enabled (set CCQUOTA_INGEST_TOKEN)", http.StatusBadRequest)
		return
	}
	var req enrollReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.User == "" {
		jsonError(w, "user is required", http.StatusBadRequest)
		return
	}

	// 產生 URL-safe 隨機 token（~24 chars）
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		jsonError(w, "token generation failed", http.StatusInternalServerError)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	expiresAt := time.Now().Unix() + 24*3600
	if err := h.s.CreateEnrollment(token, req.Account, req.User, expiresAt); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	base := h.baseURL(r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"url":        base + "/e/" + token,
		"expires_at": expiresAt,
	})
}

// enrollScriptTmpl 是 GET /e/<token> 回傳的 shell script 模板。
var enrollScriptTmpl = template.Must(template.New("enroll").Parse(`#!/usr/bin/env bash
# ccquota enrollment script (自動產生，請勿手動編輯)
set -euo pipefail

LANG_SEL="${CCQUOTA_LANG:-en}"

msg() {
  local key="$1" extra="${2:-}"
  case "$LANG_SEL" in
    zh-TW)
      case "$key" in
        need_jq)  echo "錯誤：需要 jq，請先安裝 (brew install jq)" ;;
        backed_up) echo "已備份設定檔至：$extra" ;;
        created)  echo "已建立新設定檔：$extra" ;;
        done)     echo "✓ 安裝完成！請重新啟動 Claude Code 以套用設定。" ;;
        restart)  echo "提示：關閉並重新開啟 Claude Code。" ;;
        *)        echo "$key $extra" ;;
      esac ;;
    zh-CN)
      case "$key" in
        need_jq)  echo "错误：需要 jq，请先安装 (brew install jq)" ;;
        backed_up) echo "已备份配置文件至：$extra" ;;
        created)  echo "已创建新配置文件：$extra" ;;
        done)     echo "✓ 安装完成！请重启 Claude Code 以应用配置。" ;;
        restart)  echo "提示：关闭并重新打开 Claude Code。" ;;
        *)        echo "$key $extra" ;;
      esac ;;
    *)
      case "$key" in
        need_jq)  echo "Error: jq is required. Install it first (brew install jq)" ;;
        backed_up) echo "Backed up settings to: $extra" ;;
        created)  echo "Created settings file: $extra" ;;
        done)     echo "✓ Installation complete! Restart Claude Code to apply settings." ;;
        restart)  echo "Hint: close and reopen Claude Code." ;;
        *)        echo "$key $extra" ;;
      esac ;;
  esac
}

if ! command -v jq &>/dev/null; then
  msg need_jq >&2
  exit 1
fi

SERVER={{.Server}}
ACCOUNT={{.Account}}
USER_NAME={{.User}}
TOKEN={{.Token}}

if [[ -n "${CLAUDE_CONFIG_DIR:-}" ]]; then
  SETTINGS_FILE="$CLAUDE_CONFIG_DIR/settings.json"
else
  SETTINGS_FILE="$HOME/.claude/settings.json"
fi
mkdir -p "$(dirname "$SETTINGS_FILE")"

BACKUP="${SETTINGS_FILE}.bak-$(date +%s)"
if [[ -f "$SETTINGS_FILE" ]]; then
  cp "$SETTINGS_FILE" "$BACKUP"
  msg backed_up "$BACKUP"
else
  echo "{}" > "$SETTINGS_FILE"
  msg created "$SETTINGS_FILE"
  cp "$SETTINGS_FILE" "$BACKUP"
fi

UPDATED=$(jq \
  --arg server  "$SERVER" \
  --arg account "$ACCOUNT" \
  --arg user    "$USER_NAME" \
  --arg token   "$TOKEN" \
  '
  .env //= {} |
  .env["CLAUDE_CODE_ENABLE_TELEMETRY"]                       = "1" |
  .env["OTEL_METRICS_EXPORTER"]                              = "otlp" |
  .env["OTEL_EXPORTER_OTLP_PROTOCOL"]                       = "http/protobuf" |
  .env["OTEL_EXPORTER_OTLP_ENDPOINT"]                       = $server |
  .env["OTEL_EXPORTER_OTLP_HEADERS"]                        = ("Authorization=Bearer " + $token) |
  .env["OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"] = "delta" |
  .env["OTEL_RESOURCE_ATTRIBUTES"]                          = ("ccquota.account=" + $account + ",ccquota.user=" + $user)
  ' "$SETTINGS_FILE")

printf '%s\n' "$UPDATED" > "$SETTINGS_FILE"

msg done
msg restart
`))

// scriptData 是 enrollScriptTmpl 的資料結構，所有值已 shell-quote。
type scriptData struct {
	Server  string
	Account string
	User    string
	Token   string
}

// shellQuote 對字串做單引號 shell escape。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func (h *handler) handleEnrollScript(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/e/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}

	now := time.Now().Unix()
	accountID, user, ok, err := h.s.GetEnrollment(token, now)
	if err != nil || !ok {
		http.Error(w, "invalid or expired enrollment link", http.StatusNotFound)
		return
	}

	base := h.baseURL(r)
	w.Header().Set("Content-Type", "text/x-shellscript")
	enrollScriptTmpl.Execute(w, scriptData{
		Server:  shellQuote(base),
		Account: shellQuote(accountID),
		User:    shellQuote(user),
		Token:   shellQuote(h.ingestToken),
	})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
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

type channelView struct {
	ID      int64             `json:"id"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"` // secret 已遮蔽
}

// telegram 的 secret 欄位集合(其餘型別之後擴充)。
var secretFields = map[string][]string{
	"telegram": {"bot_token"},
	"webhook":  {},
}

// handleNotifications: GET 回頻道(遮罩)+ 門檻。
func (h *handler) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	chs, err := h.s.ListChannels()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	views := make([]channelView, 0, len(chs))
	for _, ch := range chs {
		var raw map[string]string
		json.Unmarshal([]byte(ch.Config), &raw)
		masked := make(map[string]string, len(raw))
		secrets := map[string]bool{}
		for _, f := range secretFields[ch.Type] {
			secrets[f] = true
		}
		for k, v := range raw {
			if secrets[k] {
				pt, _ := h.cipher.Decrypt(v)
				masked[k] = maskSecret(pt)
			} else {
				masked[k] = v
			}
		}
		views = append(views, channelView{ID: ch.ID, Type: ch.Type, Enabled: ch.Enabled, Config: masked})
	}
	th, _ := h.s.GetAlertThresholds()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"channels": views, "thresholds": th})
}

type channelReq struct {
	ID      int64             `json:"id"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// encryptConfig 把 secret 欄位加密;沒重送(空字串)的 secret 保留舊值 prev。
func (h *handler) encryptConfig(chType string, in map[string]string, prev map[string]string) (string, error) {
	secrets := map[string]bool{}
	for _, f := range secretFields[chType] {
		secrets[f] = true
	}
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
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req channelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if _, ok := secretFields[req.Type]; !ok {
		http.Error(w, "unknown channel type", 400)
		return
	}
	cfg, err := h.encryptConfig(req.Type, req.Config, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	id, err := h.s.CreateChannel(store.Channel{Type: req.Type, Config: cfg, Enabled: req.Enabled})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"id": id})
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
		http.Error(w, err.Error(), 500)
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
			http.Error(w, "bad json", 400)
			return
		}
		var prev map[string]string
		json.Unmarshal([]byte(cur.Config), &prev)
		cfg, err := h.encryptConfig(cur.Type, req.Config, prev)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := h.s.UpdateChannel(store.Channel{ID: id, Type: cur.Type, Config: cfg, Enabled: req.Enabled}); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case r.Method == http.MethodDelete:
		if err := h.s.DeleteChannel(id); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// testChannel 用該頻道送一則測試訊息。
func (h *handler) testChannel(w http.ResponseWriter, r *http.Request, ch store.Channel) {
	var raw map[string]string
	json.Unmarshal([]byte(ch.Config), &raw)
	dec := make(map[string]string, len(raw))
	for k, v := range raw {
		pt, _ := h.cipher.Decrypt(v)
		dec[k] = pt
	}
	sink, err := alert.BuildSink(ch.Type, dec)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := sink.Send(r.Context(), "ccquota test message ✅"); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleThresholds: PUT 更新門檻。
func (h *handler) handleThresholds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var t store.AlertThresholds
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if err := h.s.SetAlertThresholds(t); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

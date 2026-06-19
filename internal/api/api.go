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

	"github.com/ccquota/ccquota/internal/calc"
	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/secret"
	"github.com/ccquota/ccquota/internal/share"
	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/update"
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
	version     string
	mu          sync.Mutex
	pending     map[string]pendingLogin

	updMu  sync.Mutex
	updRel update.Release
	updAt  time.Time
}

// New returns an http.Handler with all API routes mounted.
// staleSec 為帳號資料視為過時的秒數閾值（對應 CCQUOTA_POLLER_STALE_SEC）。
// ingestToken 為 /v1/metrics 的 Bearer token（空字串 = 關閉 enroll 功能）。
// publicURL 為對外 URL（空字串時從 request 自動推導）。
func New(s *store.Store, oc *oauth.Client, staleSec int64, ingestToken, publicURL string, cipher *secret.Cipher, version string) http.Handler {
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
		version:     version,
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
	mux.Handle("/api/version", adminAuth(s, http.HandlerFunc(h.handleVersion)))
	mux.Handle("/api/update", adminAuth(s, http.HandlerFunc(h.handleUpdate)))
	// /e/<token>: enrollment script，不需要 admin auth
	mux.HandleFunc("/e/", h.handleEnrollScript)
	// 通知設定端點
	mux.Handle("/api/notifications", adminAuth(s, http.HandlerFunc(h.handleNotifications)))
	mux.Handle("/api/notifications/channels", adminAuth(s, http.HandlerFunc(h.handleChannelsCollection)))
	mux.Handle("/api/notifications/channels/", adminAuth(s, http.HandlerFunc(h.handleChannelItem)))
	mux.Handle("/api/notifications/thresholds", adminAuth(s, http.HandlerFunc(h.handleThresholds)))
	mux.Handle("/api/user-series", adminAuth(s, http.HandlerFunc(h.handleUserSeries)))
	mux.Handle("/api/users", adminAuth(s, http.HandlerFunc(h.handleDeleteUser)))
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
		// 不送 WWW-Authenticate:否則瀏覽器會跳原生 Basic Auth 彈窗。
		// curl -u 仍可用(它會預先帶 Authorization header,不需要伺服器宣告)。
		jsonError(w, "unauthorized", http.StatusUnauthorized)
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
	writeJSON(w, map[string]bool{"ok": true})
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
	writeJSON(w, map[string]bool{"ok": true})
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
	writeJSON(w, map[string]any{
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
	writeJSON(w, map[string]bool{"ok": true})
}

// userCostResp 代表單一使用者的成本明細。
// SharePct 是額度使用率（花費 ÷ 人均週額度）；TokenSharePct 是 token 佔全體的比例（加總 100%）。
// TokenBudgetPct 是 token 平分佔比（$ 還沒夠的冷啟動,dashboard 改用它當「額度使用率」）。
type userCostResp struct {
	User           string  `json:"user"`
	CostUSD        float64 `json:"cost_usd"`
	Tokens         int64   `json:"tokens"`
	SharePct       float64 `json:"share_pct"`
	TokenSharePct  float64 `json:"token_share_pct"`
	TokenBudgetPct float64 `json:"token_budget_pct"`
}

// costResp 代表帳號期間成本與反推週額度資訊。
// Token* 是平行軌:$ 還沒累積夠（weekly_budget_usd≈0）時,dashboard 改顯示 token 反推額度與佔比。
type costResp struct {
	PeriodCostUSD      float64        `json:"period_cost_usd"`
	WeeklyBudgetUSD    float64        `json:"weekly_budget_usd"`
	LastWeekBudgetUSD  float64        `json:"last_week_budget_usd"`
	PerUserBudgetUSD   float64        `json:"per_user_budget_usd"`
	TokenWeeklyBudget  float64        `json:"token_weekly_budget"`
	PerUserTokenBudget float64        `json:"per_user_token_budget"`
	Users              []userCostResp `json:"users"`
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

		// 決定期間起始時間(與 serve loop 告警共用 share.SinceTS,確保視窗一致)。
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
		}
		sinceTS := share.SinceTS(reading, ok, now)

		// 計算期間成本與反推週額度。
		costInfo, err := h.buildCost(a.ID, sinceTS, now, reading, ok)
		if err != nil {
			jsonError(w, "db error", http.StatusInternalServerError)
			return
		}
		ar.Cost = costInfo

		out = append(out, ar)
	}
	writeJSON(w, out)
}

// rosterWindowSec:dashboard 名單保留「最近這段期間用過的人」,
// 讓週限重置後當前視窗清空時,名單不會整個消失(沒新用量者顯示 $0 直到再次使用)。
const rosterWindowSec = 7 * 24 * 3600

// buildCost 計算帳號的期間成本、反推週額度與每人份額。
// per-user 份額計算抽到 share.Compute,與 serve loop 的告警共用同一套(防分岔)。
func (h *handler) buildCost(accountID string, sinceTS, now int64, reading store.Reading, hasReading bool) (costResp, error) {
	var sevenDayPct float64
	if hasReading {
		sevenDayPct = reading.SevenDay
	}
	baseline, err := h.s.BudgetHWM(accountID)
	if err != nil {
		return costResp{}, err
	}
	res, err := share.Compute(h.s, accountID, sinceTS, sevenDayPct, baseline)
	if err != nil {
		return costResp{}, err
	}
	lastWeek, err := h.s.LastWeekBudget(accountID)
	if err != nil {
		return costResp{}, err
	}

	// token 佔比的分母：全體使用者 token 總和(dashboard 專用,告警不需要)。
	var totalTokens int64
	for _, s := range res.Shares {
		totalTokens += s.Tokens
	}

	users := make([]userCostResp, 0, len(res.Shares))
	seen := make(map[string]bool, len(res.Shares))
	for _, s := range res.Shares {
		seen[s.User] = true
		users = append(users, userCostResp{
			User:           s.User,
			CostUSD:        s.Cost,
			Tokens:         s.Tokens,
			SharePct:       s.SharePct,
			TokenSharePct:  calc.TokenSharePct(s.Tokens, totalTokens),
			TokenBudgetPct: s.TokenBudgetPct,
		})
	}

	// 補進最近 rosterWindowSec 用過、但當前視窗沒花錢的人(顯示 $0)。
	// 純顯示用,不進 share.Compute 的分母,故不稀釋 perUserBudget。
	roster, err := h.s.DistinctUsersSince(accountID, now-rosterWindowSec)
	if err != nil {
		return costResp{}, err
	}
	for _, u := range roster {
		if !seen[u] {
			users = append(users, userCostResp{User: u})
		}
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].CostUSD > users[j].CostUSD
	})

	return costResp{
		PeriodCostUSD:      res.PeriodCost,
		WeeklyBudgetUSD:    res.EffectiveBudget,
		LastWeekBudgetUSD:  lastWeek,
		PerUserBudgetUSD:   res.PerUserBudget,
		TokenWeeklyBudget:  res.TokenWeeklyBudget,
		PerUserTokenBudget: res.PerUserTokenBudget,
		Users:              users,
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
	writeJSON(w, out)
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

	writeJSON(w, map[string]string{
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

	writeJSON(w, map[string]bool{"ok": true})
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

	ttlDays := envInt64Default("CCQUOTA_ENROLL_TTL_DAYS", 30)
	expiresAt := time.Now().Unix() + ttlDays*24*3600
	if err := h.s.CreateEnrollment(token, req.Account, req.User, expiresAt); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	base := h.baseURL(r)
	writeJSON(w, map[string]any{
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

RAW_BASE="${CCQUOTA_REPO_RAW:-https://raw.githubusercontent.com/homieyangg/ccquota/main}"

# ── ccquota statusline(在 Claude Code statusline 顯示額度)──────────────────────
# 由 wrap 當統一入口:有既有 statusLine 就包起來、跑完接上 ccquota 個人 share;沒有就出完整 ccquota 行。
SL="$HOME/.ccquota/statusline.sh"
WRAP="$HOME/.ccquota/statusline-wrap.sh"
if curl -fsSL "$RAW_BASE/scripts/statusline.sh" -o "$SL" 2>/dev/null; then
  chmod +x "$SL"
  curl -fsSL "$RAW_BASE/scripts/statusline-wrap.sh" -o "$WRAP" 2>/dev/null && chmod +x "$WRAP"
  {
    printf 'CCQUOTA_SERVER=%q\n'  "$SERVER"
    printf 'CCQUOTA_ACCOUNT=%q\n' "$ACCOUNT"
    printf 'CCQUOTA_USER=%q\n'    "$USER_NAME"
    printf 'CCQUOTA_TOKEN=%q\n'   "$TOKEN"
  } > "$HOME/.ccquota/config"
  chmod 600 "$HOME/.ccquota/config"
  WRAP_CMD=$(printf 'bash %q' "$WRAP")
  existing=$(jq -r '.statusLine.command // empty' "$SETTINGS_FILE" 2>/dev/null)
  case "$existing" in
    *.ccquota/statusline-wrap.sh*)
      echo "✓ ccquota statusline(已設定)" ;;
    "")
      rm -f "$HOME/.ccquota/statusline-orig"
      UPDATED=$(jq --arg cmd "$WRAP_CMD" '.statusLine = {"type":"command","command":$cmd}' "$SETTINGS_FILE")
      printf '%s\n' "$UPDATED" > "$SETTINGS_FILE"
      echo "✓ ccquota statusline" ;;
    *)
      printf '%s' "$existing" > "$HOME/.ccquota/statusline-orig"
      UPDATED=$(jq --arg cmd "$WRAP_CMD" '.statusLine.command = $cmd' "$SETTINGS_FILE")
      printf '%s\n' "$UPDATED" > "$SETTINGS_FILE"
      echo "✓ ccquota statusline(已接在你原本的 statusline 後)" ;;
  esac
fi

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

// envInt64Default 讀 env int64，空或非法則回 def。
func envInt64Default(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

type seriesPoint struct {
	TS     int64   `json:"ts"`
	Cost   float64 `json:"cost_usd"`
	Tokens int64   `json:"tokens"`
}

// handleUserSeries: GET /api/user-series?account=&user=&range=24h|7d
// 回傳 {bucket_sec, points:[{ts,cost_usd,tokens}]}，桶為零填（連續）。
func (h *handler) handleUserSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	acct := r.URL.Query().Get("account")
	user := r.URL.Query().Get("user")
	if acct == "" || user == "" {
		jsonError(w, "account and user required", http.StatusBadRequest)
		return
	}
	var rangeSec, bucketSec int64 = 24 * 3600, 600
	if r.URL.Query().Get("range") == "7d" {
		rangeSec, bucketSec = 7*24*3600, 7200
	}
	now := time.Now().Unix()
	sinceTS := now - rangeSec
	buckets, err := h.s.UserSeries(acct, user, sinceTS, bucketSec)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	byTS := make(map[int64]store.SeriesBucket, len(buckets))
	for _, b := range buckets {
		byTS[b.TS] = b
	}
	start := (sinceTS / bucketSec) * bucketSec
	end := (now / bucketSec) * bucketSec
	points := make([]seriesPoint, 0, (end-start)/bucketSec+1)
	for t := start; t <= end; t += bucketSec {
		if b, ok := byTS[t]; ok {
			points = append(points, seriesPoint{TS: t, Cost: b.Cost, Tokens: b.Tokens})
		} else {
			points = append(points, seriesPoint{TS: t, Cost: 0, Tokens: 0})
		}
	}
	writeJSON(w, map[string]any{"bucket_sec": bucketSec, "points": points})
}

// handleDeleteUser: DELETE /api/users?account=&user=
func (h *handler) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	acct := r.URL.Query().Get("account")
	user := r.URL.Query().Get("user")
	if acct == "" || user == "" {
		jsonError(w, "account and user required", http.StatusBadRequest)
		return
	}
	if err := h.s.DeleteUser(acct, user); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

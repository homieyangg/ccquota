package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ccquota/ccquota/internal/calc"
	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/store"
)

// AdminPassword is the HTTP Basic Auth password in use (may be auto-generated).
var AdminPassword string

func init() {
	AdminPassword = os.Getenv("CCQUOTA_ADMIN_PASSWORD")
	if AdminPassword == "" {
		b := make([]byte, 16)
		rand.Read(b)
		AdminPassword = hex.EncodeToString(b)
	}
}

type pendingLogin struct {
	verifier  string
	state     string
	createdAt time.Time
}

type handler struct {
	s        *store.Store
	oc       *oauth.Client
	staleSec int64
	mu       sync.Mutex
	pending  map[string]pendingLogin
}

// New returns an http.Handler with all API routes mounted.
// staleSec 為帳號資料視為過時的秒數閾值（對應 CCQUOTA_POLLER_STALE_SEC）。
func New(s *store.Store, oc *oauth.Client, staleSec int64) http.Handler {
	h := &handler{
		s:        s,
		oc:       oc,
		staleSec: staleSec,
		pending:  make(map[string]pendingLogin),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/api/accounts", basicAuth(http.HandlerFunc(h.handleAccounts)))
	mux.Handle("/api/history", basicAuth(http.HandlerFunc(h.handleHistory)))
	mux.Handle("/api/login/start", basicAuth(http.HandlerFunc(h.handleLoginStart)))
	mux.Handle("/api/login/complete", basicAuth(http.HandlerFunc(h.handleLoginComplete)))
	return mux
}

func basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(AdminPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="ccquota"`)
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
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

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

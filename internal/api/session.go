package api

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

const (
	sessionCookieName = "ccquota_session"
	sessionTTL        = 7 * 24 * time.Hour
)

// sessions 是 in-memory session 儲存，token → expiry unix 時間戳。
type sessions struct {
	mu   sync.Mutex
	data map[string]int64
}

var globalSessions = &sessions{data: make(map[string]int64)}

// create 建立新 session，回傳 token（32-byte url-safe base64）。
func (ss *sessions) create() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expiry := time.Now().Add(sessionTTL).Unix()

	ss.mu.Lock()
	defer ss.mu.Unlock()
	// 順帶清掉過期 session
	now := time.Now().Unix()
	for k, v := range ss.data {
		if v < now {
			delete(ss.data, k)
		}
	}
	ss.data[token] = expiry
	return token, nil
}

// validate 確認 token 存在且未過期。
func (ss *sessions) validate(token string) bool {
	if token == "" {
		return false
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	expiry, ok := ss.data[token]
	if !ok {
		return false
	}
	if time.Now().Unix() > expiry {
		delete(ss.data, token)
		return false
	}
	return true
}

// delete 刪除 session。
func (ss *sessions) delete(token string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.data, token)
}

// isSecure 判斷此 request 是否走 HTTPS。
func isSecure(r *http.Request) bool {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		return true
	}
	return r.TLS != nil
}

// setSessionCookie 在 response 上設置 session cookie。
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	c := &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	}
	http.SetCookie(w, c)
}

// clearSessionCookie 讓 cookie 立即過期（登出用）。
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   0,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	}
	http.SetCookie(w, c)
}

// hasValidSession 從 request 的 cookie 中取出並驗證 session。
func hasValidSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return globalSessions.validate(c.Value)
}

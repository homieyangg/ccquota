package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName = "ccquota_session"
	sessionTTL        = 7 * 24 * time.Hour
)

// sessionSecret 是簽 session token 的金鑰(持久化,跨重啟/自我更新不變)。
// 由 main 在啟動時以 SetSessionSecret 設成 secret.LoadKey 的金鑰。
var sessionSecret []byte

// SetSessionSecret 設定 session 簽章金鑰。
func SetSessionSecret(k []byte) { sessionSecret = k }

// signSession 產生「<expiry>.<hmac>」格式的無狀態 token。
func signSession(expiry int64) string {
	msg := strconv.FormatInt(expiry, 10)
	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write([]byte(msg))
	return msg + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validSession 驗證無狀態 token:未過期且 HMAC 正確。
func validSession(token string) bool {
	i := strings.LastIndex(token, ".")
	if i <= 0 {
		return false
	}
	msg, sig := token[:i], token[i+1:]
	exp, err := strconv.ParseInt(msg, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write([]byte(msg))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1
}

// revoked 是登出 token 的盡力撤銷表(token -> 其到期 unix 時間)。
// 無狀態 token 本身會在到期前一直有效;登出時把它記進來主動擋掉。
// 重啟會清空,但登出已清掉 cookie,瀏覽器手上沒有 token,實務上無妨。
var revoked = struct {
	mu sync.Mutex
	m  map[string]int64
}{m: map[string]int64{}}

// sessions 是無狀態 session 的薄包裝(保留既有呼叫介面)。
type sessions struct{}

var globalSessions = &sessions{}

// create 簽出一個有效期 sessionTTL 的 token。
func (ss *sessions) create() (string, error) {
	return signSession(time.Now().Add(sessionTTL).Unix()), nil
}

// delete 撤銷 token(登出)。
func (ss *sessions) delete(token string) {
	exp := tokenExpiry(token)
	revoked.mu.Lock()
	defer revoked.mu.Unlock()
	now := time.Now().Unix()
	for k, v := range revoked.m { // 順帶清掉已過期的
		if v < now {
			delete(revoked.m, k)
		}
	}
	if exp > now {
		revoked.m[token] = exp
	}
}

// validate 驗證 token:HMAC 正確、未過期、未被撤銷。
func (ss *sessions) validate(token string) bool {
	if !validSession(token) {
		return false
	}
	revoked.mu.Lock()
	_, gone := revoked.m[token]
	revoked.mu.Unlock()
	return !gone
}

// tokenExpiry 取 token 內嵌的到期時間(解析失敗回 0)。
func tokenExpiry(token string) int64 {
	i := strings.LastIndex(token, ".")
	if i <= 0 {
		return 0
	}
	exp, _ := strconv.ParseInt(token[:i], 10, 64)
	return exp
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

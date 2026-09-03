package poller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccquota/ccquota/internal/oauth"
	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
)

type tokenCaptureUsage struct{ lastToken string }

func (u *tokenCaptureUsage) Fetch(_ context.Context, t string) (usage.Snapshot, error) {
	u.lastToken = t
	return usage.Snapshot{SevenDay: 50}, nil
}

// credsRefresher 回傳固定的新 token,並記下收到的 refresh token。
type credsRefresher struct {
	got  string
	tok  oauth.Token
	err  error
	call int
}

func (r *credsRefresher) Refresh(_ context.Context, rt string) (oauth.Token, error) {
	r.call++
	r.got = rt
	return r.tok, r.err
}

func writeCreds(t *testing.T, path, token, refresh string, expMs int64) {
	t.Helper()
	d := map[string]any{credsKey: map[string]any{
		"accessToken":  token,
		"refreshToken": refresh,
		"expiresAt":    expMs,
		"scopes":       []string{"user:inference"}, // 非 poller 欄位,用來驗證寫回不會弄丟
	}}
	b, _ := json.Marshal(d)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOauthField(t *testing.T, path, field string) any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc[credsKey].(map[string]any)[field]
}

// TestCycleCLIBacked:CredsPath 設定時,poller 讀本機 creds 的 token 打 usage 寫 reading,
// token 新鮮時不觸發 refresh。
func TestCycleCLIBacked(t *testing.T) {
	credsPath := filepath.Join(t.TempDir(), "creds.json")
	writeCreds(t, credsPath, "AT-CLI", "RT", (time.Now().Unix()+8*3600)*1000) // 新鮮
	s, _ := store.Open(":memory:")
	defer s.Close()
	u := &tokenCaptureUsage{}
	r := &credsRefresher{}
	p := &Poller{Store: s, Usage: u, OAuth: r, Now: func() int64 { return time.Now().Unix() }}
	if err := p.cycle(context.Background(), store.Account{ID: "main", CredsPath: credsPath}); err != nil {
		t.Fatal(err)
	}
	if u.lastToken != "AT-CLI" {
		t.Errorf("應用 creds 檔 token 打 usage,得 %q", u.lastToken)
	}
	if r.call != 0 {
		t.Error("token 新鮮不該觸發 refresh")
	}
	if _, ok, _ := s.LatestReading("main"); !ok {
		t.Error("應寫入 reading")
	}
}

// TestCycleCLIBackedNearExpiryRefreshes:快到期時自行 refresh,用新 token 打 usage,
// 並把輪替後的 token 寫回 creds 檔(其他欄位保留)。
func TestCycleCLIBackedNearExpiryRefreshes(t *testing.T) {
	credsPath := filepath.Join(t.TempDir(), "creds.json")
	writeCreds(t, credsPath, "AT-OLD", "RT-OLD", (time.Now().Unix()+60)*1000) // 剩 60s
	s, _ := store.Open(":memory:")
	defer s.Close()
	u := &tokenCaptureUsage{}
	r := &credsRefresher{tok: oauth.Token{AccessToken: "AT-NEW", RefreshToken: "RT-NEW", ExpiresIn: 28800}}
	now := time.Now().Unix()
	p := &Poller{Store: s, Usage: u, OAuth: r, Now: func() int64 { return now }}
	if err := p.cycle(context.Background(), store.Account{ID: "main", CredsPath: credsPath}); err != nil {
		t.Fatal(err)
	}
	if r.call != 1 || r.got != "RT-OLD" {
		t.Errorf("應帶舊 refresh token 換新,call=%d got=%q", r.call, r.got)
	}
	if u.lastToken != "AT-NEW" {
		t.Errorf("refresh 後應用新 token,得 %q", u.lastToken)
	}
	if got := readOauthField(t, credsPath, "accessToken"); got != "AT-NEW" {
		t.Errorf("creds 檔 accessToken 應更新,得 %v", got)
	}
	if got := readOauthField(t, credsPath, "refreshToken"); got != "RT-NEW" {
		t.Errorf("creds 檔 refreshToken 應更新,得 %v", got)
	}
	if got := readOauthField(t, credsPath, "expiresAt"); got != float64((now+28800)*1000) {
		t.Errorf("creds 檔 expiresAt 應為毫秒新到期,得 %v", got)
	}
	if readOauthField(t, credsPath, "scopes") == nil {
		t.Error("寫回不該弄丟其他欄位")
	}
}

// TestCycleCLIBackedLoggedOut:CLI 登出(兩顆 token 都空)時直接回報要重登,不打任何外部請求。
func TestCycleCLIBackedLoggedOut(t *testing.T) {
	credsPath := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(credsPath, []byte(`{"other":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := store.Open(":memory:")
	defer s.Close()
	u := &tokenCaptureUsage{}
	r := &credsRefresher{}
	p := &Poller{Store: s, Usage: u, OAuth: r, Now: func() int64 { return time.Now().Unix() }}
	err := p.cycle(context.Background(), store.Account{ID: "main", CredsPath: credsPath})
	if err == nil {
		t.Fatal("登出應回錯誤")
	}
	if r.call != 0 {
		t.Error("登出不該打 token endpoint")
	}
	if u.lastToken != "" {
		t.Error("登出不該打 usage")
	}
}

// TestCycleCLIBackedRefreshFailKeepsToken:refresh 失敗但舊 token 還沒過期時,
// continue 用舊 token,不中斷這一輪。
func TestCycleCLIBackedRefreshFailKeepsToken(t *testing.T) {
	credsPath := filepath.Join(t.TempDir(), "creds.json")
	writeCreds(t, credsPath, "AT-OLD", "RT-OLD", (time.Now().Unix()+600)*1000) // 還有 10 分
	s, _ := store.Open(":memory:")
	defer s.Close()
	u := &tokenCaptureUsage{}
	r := &credsRefresher{err: errors.New("boom")}
	p := &Poller{Store: s, Usage: u, OAuth: r, Now: func() int64 { return time.Now().Unix() }}
	if err := p.cycle(context.Background(), store.Account{ID: "main", CredsPath: credsPath}); err != nil {
		t.Fatal(err)
	}
	if u.lastToken != "AT-OLD" {
		t.Errorf("refresh 失敗應沿用舊 token,得 %q", u.lastToken)
	}
	if got := readOauthField(t, credsPath, "accessToken"); got != "AT-OLD" {
		t.Errorf("refresh 失敗不該動 creds 檔,得 %v", got)
	}
}

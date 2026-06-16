package poller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccquota/ccquota/internal/store"
	"github.com/ccquota/ccquota/internal/usage"
)

type tokenCaptureUsage struct{ lastToken string }

func (u *tokenCaptureUsage) Fetch(_ context.Context, t string) (usage.Snapshot, error) {
	u.lastToken = t
	return usage.Snapshot{SevenDay: 50}, nil
}

func writeCreds(t *testing.T, path, token string, expMs int64) {
	t.Helper()
	d := map[string]any{"claudeAiOauth": map[string]any{"accessToken": token, "expiresAt": expMs}}
	b, _ := json.Marshal(d)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCycleCLIBacked:CredsPath 設定時,poller 讀本機 creds 的 token 打 usage 寫 reading,
// token 新鮮時不觸發 refresh。
func TestCycleCLIBacked(t *testing.T) {
	credsPath := filepath.Join(t.TempDir(), "creds.json")
	writeCreds(t, credsPath, "AT-CLI", (time.Now().Unix()+8*3600)*1000) // 新鮮
	s, _ := store.Open(":memory:")
	defer s.Close()
	u := &tokenCaptureUsage{}
	refreshCalled := false
	p := &Poller{
		Store: s, Usage: u, Now: func() int64 { return time.Now().Unix() },
		RefreshCmd: func(context.Context) error { refreshCalled = true; return nil },
	}
	if err := p.cycle(context.Background(), store.Account{ID: "main", CredsPath: credsPath}); err != nil {
		t.Fatal(err)
	}
	if u.lastToken != "AT-CLI" {
		t.Errorf("應用 creds 檔 token 打 usage,得 %q", u.lastToken)
	}
	if refreshCalled {
		t.Error("token 新鮮不該觸發 refresh")
	}
	if _, ok, _ := s.LatestReading("main"); !ok {
		t.Error("應寫入 reading")
	}
}

// TestCycleCLIBackedNearExpiryRefreshes:快到期時跑 RefreshCmd,refresh 後用新 token。
func TestCycleCLIBackedNearExpiryRefreshes(t *testing.T) {
	credsPath := filepath.Join(t.TempDir(), "creds.json")
	writeCreds(t, credsPath, "AT-OLD", (time.Now().Unix()+60)*1000) // 剩 60s,快到期
	s, _ := store.Open(":memory:")
	defer s.Close()
	u := &tokenCaptureUsage{}
	refreshCalled := false
	p := &Poller{
		Store: s, Usage: u, Now: func() int64 { return time.Now().Unix() },
		RefreshCmd: func(context.Context) error {
			refreshCalled = true
			writeCreds(t, credsPath, "AT-NEW", (time.Now().Unix()+8*3600)*1000) // 模擬 doctor refresh
			return nil
		},
	}
	if err := p.cycle(context.Background(), store.Account{ID: "main", CredsPath: credsPath}); err != nil {
		t.Fatal(err)
	}
	if !refreshCalled {
		t.Error("快到期應觸發 refresh")
	}
	if u.lastToken != "AT-NEW" {
		t.Errorf("refresh 後應用新 token,得 %q", u.lastToken)
	}
}

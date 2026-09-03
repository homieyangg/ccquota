package poller

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccquota/ccquota/internal/oauth"
)

// credsKey 是 claude CLI creds 檔裡放 OAuth 的欄位名。
const credsKey = "claudeAiOauth"

// credsFile 對應 claude CLI 的 ~/.claude/.credentials.json 結構(只取需要的欄位)。
type credsFile struct {
	ClaudeAiOauth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

// readCredsToken 從 claude CLI creds 檔讀 access/refresh token 與到期(回 unix 秒)。
// expiresAt 可能是毫秒,自動轉秒。
func readCredsToken(path string) (token, refresh string, expiresAt int64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, err
	}
	var c credsFile
	if err := json.Unmarshal(b, &c); err != nil {
		return "", "", 0, err
	}
	exp := c.ClaudeAiOauth.ExpiresAt
	if exp > 100000000000 { // 毫秒 → 秒
		exp /= 1000
	}
	return c.ClaudeAiOauth.AccessToken, c.ClaudeAiOauth.RefreshToken, exp, nil
}

// writeCredsToken 把 refresh 換來的新 token 寫回 creds 檔,讓 CLI 與 poller 共用同一顆。
// 只覆蓋 accessToken/refreshToken/expiresAt,其餘欄位(scopes、subscriptionType 等)原樣保留。
// 用同目錄暫存檔 + rename 落地,避免寫到一半被中斷留下半截檔案。
func writeCredsToken(path string, tok oauth.Token, now int64) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	oa, _ := doc[credsKey].(map[string]any)
	if oa == nil {
		oa = map[string]any{}
	}
	oa["accessToken"] = tok.AccessToken
	oa["refreshToken"] = tok.RefreshToken
	oa["expiresAt"] = (now + tok.ExpiresIn) * 1000
	doc[credsKey] = oa

	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

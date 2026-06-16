package poller

import (
	"encoding/json"
	"os"
)

// credsFile 對應 claude CLI 的 ~/.claude/.credentials.json 結構(只取需要的欄位)。
type credsFile struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

// readCredsToken 從 claude CLI creds 檔讀 access token 與到期(回 unix 秒)。
// expiresAt 可能是毫秒,自動轉秒。
func readCredsToken(path string) (token string, expiresAt int64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	var c credsFile
	if err := json.Unmarshal(b, &c); err != nil {
		return "", 0, err
	}
	exp := c.ClaudeAiOauth.ExpiresAt
	if exp > 100000000000 { // 毫秒 → 秒
		exp /= 1000
	}
	return c.ClaudeAiOauth.AccessToken, exp, nil
}

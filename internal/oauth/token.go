package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	HTTP     *http.Client
	TokenURL string // defaults to TokenEP when empty
}

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (c *Client) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return TokenEP
}

func (c *Client) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) postToken(ctx context.Context, payload map[string]string) (Token, error) {
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), bytes.NewReader(buf))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.httpc().Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return Token{}, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok Token
	if err := json.Unmarshal(body, &tok); err != nil {
		return Token{}, err
	}
	if tok.AccessToken == "" {
		return Token{}, fmt.Errorf("no access_token in response: %s", string(body))
	}
	if tok.ExpiresIn <= 0 {
		return Token{}, fmt.Errorf("invalid expires_in: %d", tok.ExpiresIn)
	}
	return tok, nil
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	tok, err := c.postToken(ctx, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     ClientID,
	})
	if err != nil {
		return Token{}, err
	}
	// Anthropic rotates the refresh token; if the response omits it, keep the old one.
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

func (c *Client) ExchangeCode(ctx context.Context, code string, p PKCE) (Token, error) {
	// The pasted code may arrive as "code#state"; keep only the code.
	if i := strings.IndexByte(code, '#'); i >= 0 {
		code = code[:i]
	}
	return c.postToken(ctx, map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  RedirectURI,
		"client_id":     ClientID,
		"code_verifier": p.Verifier,
		"state":         p.State,
	})
}

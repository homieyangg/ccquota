package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
)

const (
	ClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AuthorizeEP = "https://claude.com/cai/oauth/authorize"
	TokenEP     = "https://console.anthropic.com/v1/oauth/token"
	RedirectURI = "https://platform.claude.com/oauth/code/callback"
	Scope       = "org:create_api_key user:profile user:inference"
	UserAgent   = "claude-code/2.1.177"
)

type PKCE struct {
	Verifier  string
	Challenge string
	State     string
}

func randB64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func NewPKCE() (PKCE, error) {
	v, err := randB64URL(32)
	if err != nil {
		return PKCE{}, err
	}
	st, err := randB64URL(24)
	if err != nil {
		return PKCE{}, err
	}
	sum := sha256.Sum256([]byte(v))
	return PKCE{
		Verifier:  v,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		State:     st,
	}, nil
}

func AuthorizeURL(p PKCE) string {
	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", RedirectURI)
	q.Set("scope", Scope)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", p.State)
	return AuthorizeEP + "?" + q.Encode()
}

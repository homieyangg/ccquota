package oauth

import (
	"net/url"
	"strings"
	"testing"
)

func TestNewPKCEAndAuthorizeURL(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Verifier) < 43 || p.Challenge == "" || p.State == "" {
		t.Fatalf("weak pkce: %+v", p)
	}
	if strings.ContainsAny(p.Challenge, "+/=") {
		t.Fatalf("challenge must be base64url without padding: %q", p.Challenge)
	}
	raw := AuthorizeURL(p)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != ClientID {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("code_challenge") != p.Challenge || q.Get("code_challenge_method") != "S256" {
		t.Fatal("challenge params missing")
	}
	if !strings.Contains(q.Get("scope"), "user:profile") {
		t.Fatalf("scope must include user:profile, got %q", q.Get("scope"))
	}
}

package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExchangeCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "authorization_code" || body["code"] != "abc" {
			t.Errorf("bad body: %+v", body)
		}
		if body["code_verifier"] != "ver" {
			t.Errorf("missing verifier: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT", "refresh_token": "RT", "expires_in": 28800,
		})
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), TokenURL: srv.URL}
	tok, err := c.ExchangeCode(context.Background(), "abc", PKCE{Verifier: "ver", State: "st"})
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "AT" || tok.RefreshToken != "RT" || tok.ExpiresIn != 28800 {
		t.Fatalf("bad token: %+v", tok)
	}
}

func TestRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "OLD" {
			t.Errorf("bad body: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT2", "refresh_token": "NEW", "expires_in": 28800,
		})
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), TokenURL: srv.URL}
	tok, err := c.Refresh(context.Background(), "OLD")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "AT2" || tok.RefreshToken != "NEW" {
		t.Fatalf("bad token: %+v", tok)
	}
}

func TestRefreshKeepsOldTokenIfOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "AT2", "expires_in": 28800})
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), TokenURL: srv.URL}
	tok, err := c.Refresh(context.Background(), "OLD")
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "OLD" {
		t.Fatalf("expected old refresh token kept, got %q", tok.RefreshToken)
	}
}

package alert_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ccquota/ccquota/internal/alert"
)

func TestTelegramSink(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	sink := &alert.TelegramSink{
		Token:   "mytoken",
		ChatID:  "12345",
		BaseURL: srv.URL,
		HTTP:    srv.Client(),
	}
	if err := sink.Send(context.Background(), "hello <b>world</b>"); err != nil {
		t.Fatal(err)
	}

	wantPath := "/botmytoken/sendMessage"
	if gotPath != wantPath {
		t.Fatalf("path: got %q want %q", gotPath, wantPath)
	}

	vals, err := url.ParseQuery(gotBody)
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if vals.Get("chat_id") != "12345" {
		t.Errorf("chat_id: %q", vals.Get("chat_id"))
	}
	if vals.Get("text") != "hello <b>world</b>" {
		t.Errorf("text: %q", vals.Get("text"))
	}
	if vals.Get("parse_mode") != "HTML" {
		t.Errorf("parse_mode: %q", vals.Get("parse_mode"))
	}
}

func TestWebhookSink(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("Content-Type: %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	sink := &alert.WebhookSink{
		URL:  srv.URL,
		HTTP: srv.Client(),
	}
	if err := sink.Send(context.Background(), "test msg"); err != nil {
		t.Fatal(err)
	}

	var m map[string]string
	if err := json.Unmarshal([]byte(gotBody), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["text"] != "test msg" {
		t.Errorf("text: %q", m["text"])
	}
}

func TestTelegramSinkHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv.Close()

	sink := &alert.TelegramSink{Token: "t", ChatID: "c", BaseURL: srv.URL, HTTP: srv.Client()}
	err := sink.Send(context.Background(), "msg")
	if err == nil {
		t.Fatal("expected error on HTTP 400")
	}
}

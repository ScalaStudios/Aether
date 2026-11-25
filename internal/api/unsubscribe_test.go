package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scalastudios/aether/internal/config"
	"github.com/scalastudios/aether/internal/mail"
)

func TestChooseUnsubscribe(t *testing.T) {
	one := mail.ParseListUnsubscribe("<https://example.com/u>, <mailto:a@b.com>", "List-Unsubscribe=One-Click")
	if method, target := chooseUnsubscribe(one, false); method != "one-click" || target != "https://example.com/u" {
		t.Fatalf("one-click = %s %s", method, target)
	}
	private := mail.ParseListUnsubscribe("<https://127.0.0.1/u>, <mailto:a@b.com?subject=unsub>", "List-Unsubscribe=One-Click")
	if method, _ := chooseUnsubscribe(private, false); method != "mailto" {
		t.Fatalf("private one-click = %s, want mailto", method)
	}
	link := mail.ParseListUnsubscribe("<https://example.com/u>, <mailto:a@b.com>", "")
	if method, _ := chooseUnsubscribe(link, false); method != "link" {
		t.Fatalf("https without one-click = %s, want link", method)
	}
	onlyMail := mail.ParseListUnsubscribe("<mailto:a@b.com>", "")
	if method, _ := chooseUnsubscribe(onlyMail, false); method != "mailto" {
		t.Fatalf("mailto = %s", method)
	}
	httpOnly := mail.ParseListUnsubscribe("<http://example.com/u>", "List-Unsubscribe=One-Click")
	if method, _ := chooseUnsubscribe(httpOnly, false); method != "" {
		t.Fatalf("http without allowance = %s", method)
	}
	if method, target := chooseUnsubscribe(httpOnly, true); method != "one-click" || target != "http://example.com/u" {
		t.Fatalf("http with allowance = %s %s", method, target)
	}
	if _, err := safeUnsubscribeURL("https://user:pass@example.com/u", true); err == nil {
		t.Fatal("accepted a URL with a username")
	}
	if _, err := safeUnsubscribeURL("https://127.0.0.1/u", false); !errors.Is(err, errUnsafeUnsubscribe) {
		t.Fatalf("loopback = %v", err)
	}
	if _, err := safeUnsubscribeURL("https://example.com/u?id=1", false); err != nil {
		t.Fatal(err)
	}
}

func TestOneClickPost(t *testing.T) {
	var got, contentType, cookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		got, contentType, cookie = string(body), r.Header.Get("Content-Type"), r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{cfg: &config.Config{WebhookAllowPrivate: true}, webhookClient: newWebhookClient(true), log: log}
	if err := s.postOneClick(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if got != "List-Unsubscribe=One-Click" || contentType != "application/x-www-form-urlencoded" || cookie != "" {
		t.Fatalf("body %q type %q cookie %q", got, contentType, cookie)
	}
	blocked := &Server{webhookClient: newWebhookClient(false), log: log}
	if err := blocked.postOneClick(context.Background(), srv.URL); !errors.Is(err, errUnsafeUnsubscribe) {
		t.Fatalf("private address returned %v", err)
	}
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/other", http.StatusFound)
	}))
	defer redir.Close()
	if err := s.postOneClick(context.Background(), redir.URL); err == nil {
		t.Fatal("accepted a redirect")
	}
}

func TestNewsletterBlockedMatchesDeleteRule(t *testing.T) {
	address := "news@example.com"
	rules := []mail.Rule{{
		Enabled: true, Match: "all",
		Conditions: []mail.RuleCondition{{Field: "from", Contains: "News@Example.com"}},
		Actions:    mail.RuleActions{Delete: true, Stop: true},
	}}
	if !newsletterBlocked(rules, address) {
		t.Fatal("delete rule was not treated as a block")
	}
	rules[0].Actions.Delete = false
	if newsletterBlocked(rules, address) {
		t.Fatal("a rule that does not delete was treated as a block")
	}
	if got, ok := normalizeSenderAddress(" News@Example.com "); !ok || got != address {
		t.Fatalf("normalize = %s %v", got, ok)
	}
	if _, ok := normalizeSenderAddress("News <news@example.com>"); ok {
		t.Fatal("accepted a display name")
	}
}

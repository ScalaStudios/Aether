package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestParseSender(t *testing.T) {
	for in, want := range map[string][2]string{
		" Bad@Example.COM ": {"address", "bad@example.com"},
		"@Example.com":      {"domain", "example.com"},
		"mail.example.com":  {"domain", "mail.example.com"},
		"203.0.113.9":       {"ip", "203.0.113.9"},
		"203.0.113.9/24":    {"ip", "203.0.113.0/24"},
		"::ffff:192.0.2.1":  {"ip", "192.0.2.1"},
		"2001:db8::/32":     {"ip", "2001:db8::/32"},
	} {
		kind, value, ok := parseSender(in)
		if !ok || kind != want[0] || value != want[1] {
			t.Errorf("parseSender(%q) = %s %s %v, want %s %s", in, kind, value, ok, want[0], want[1])
		}
	}
	for _, in := range []string{"", "localhost", "Name <a@example.com>", "a@b@example.com", "300.1.1.1", "@"} {
		if _, _, ok := parseSender(in); ok {
			t.Errorf("parseSender(%q) accepted", in)
		}
	}
}

func TestSenderDomains(t *testing.T) {
	got := senderDomains([]string{"a@mx.mail.example.com", "b@example.com", "c@localhost"})
	if want := []string{"mx.mail.example.com", "mail.example.com", "example.com"}; !slices.Equal(got, want) {
		t.Errorf("senderDomains = %v, want %v", got, want)
	}
}

func TestDecideSender(t *testing.T) {
	allowDomain, allowIP, block := senderMatch{"allow", "domain", "partner.example"}, senderMatch{"allow", "ip", "203.0.113.0/24"}, senderMatch{"block", "domain", "phish.example"}
	for _, c := range []struct {
		name        string
		matches     []senderMatch
		verified    []string
		dmarcReject bool
		want        string
	}{
		{"nothing", nil, []string{"a@partner.example"}, false, ""},
		{"block wins", []senderMatch{allowDomain, block}, []string{"a@partner.example"}, false, "block"},
		{"verified subdomain sender", []senderMatch{allowDomain}, []string{"a@mail.partner.example"}, false, "allow"},
		{"unverified address", []senderMatch{allowDomain}, nil, false, ""},
		{"verified other identity", []senderMatch{allowDomain}, []string{"bounce@attacker.example"}, false, ""},
		{"unverified ip", []senderMatch{allowIP}, nil, false, "allow"},
		{"dmarc reject", []senderMatch{allowIP, allowDomain}, []string{"a@partner.example"}, true, ""},
		{"dmarc reject still blocks", []senderMatch{allowIP, block}, nil, true, "block"},
	} {
		if got := decideSender(c.matches, c.verified, c.dmarcReject); got != c.want {
			t.Errorf("%s: decideSender = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCheckSenderListsRejectsPublicClients(t *testing.T) {
	s := &Server{}
	for _, handle := range []http.HandlerFunc{s.checkSenderLists, s.checkHostedSender} {
		req := httptest.NewRequest(http.MethodPost, "/internal/sender-lists/check", strings.NewReader(`{}`))
		req.RemoteAddr = "8.8.8.8:4000"
		rec := httptest.NewRecorder()
		handle(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	}
}

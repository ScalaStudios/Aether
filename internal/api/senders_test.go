package api

import (
	"slices"
	"testing"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func TestLookalikeDomain(t *testing.T) {
	for domain, want := range map[string]bool{
		"example.com":                 false,
		"mail.example.com":            false,
		"examp1e.com":                 true,
		"exarnple.com":                true,
		"exmaple.com":                 true,
		"example.co":                  true,
		"xn--xample-2of.com":          true,
		"еxample.com":                 true,
		"secure.exampie.com":          true,
		"example.com.attacker.net":    true,
		"example.org":                 false,
		"example-billing.com":         false,
		"partner.net":                 false,
		"sample.com":                  false,
		"example.community.attack.io": false,
	} {
		if got := lookalikeDomain(domain, "example.com"); got != want {
			t.Errorf("lookalikeDomain(%q) = %v, want %v", domain, got, want)
		}
	}
	if lookalikeDomain("acme.io", "acme.co") {
		t.Error("short domains should only match homoglyphs, not single edits")
	}
	if !lookalikeDomain("acrne.co", "acme.co") {
		t.Error("homoglyph of a short domain should match")
	}
}

func TestNameKey(t *testing.T) {
	ada := nameKey("Ada Lovelace")
	for _, name := range []string{"ada lovelace", "Lovelace, Ada", "  Ada   Lovelace ", "Аda Lovelace", "Ada Lovеlace", "Ada LoveIace", "Ádá Lovelace"} {
		if got := nameKey(name); got != ada {
			t.Errorf("nameKey(%q) = %q, want %q", name, got, ada)
		}
	}
	if nameKey("Al") != "" || nameKey("") != "" {
		t.Error("names shorter than three letters should not be compared")
	}
	if nameKey("Ada Byron") == ada {
		t.Error("different names must not match")
	}
}

func TestSenderOrigin(t *testing.T) {
	domains := []string{"example.com"}
	submitted := &mailpkg.Authentication{Submission: "pass", DMARC: mailpkg.AuthResult{Result: "none"}}
	unauthenticated := &mailpkg.Authentication{Submission: "none", DMARC: mailpkg.AuthResult{Result: "fail"}}
	aligned := &mailpkg.Authentication{Submission: "none", DMARC: mailpkg.AuthResult{Result: "pass"}}
	relayed := &mailpkg.Authentication{Submission: "relay", DMARC: mailpkg.AuthResult{Result: "none"}}
	for _, tc := range []struct {
		address           string
		auth              *mailpkg.Authentication
		external, spoofed bool
	}{
		{"ada@example.com", submitted, false, false},
		{"Ada@Example.COM", aligned, false, false},
		{"ada@example.com", relayed, false, false},
		{"ada@example.com", nil, false, false},
		{"ada@example.com", unauthenticated, true, true},
		{"ada@partner.net", submitted, true, false},
		{"ada@partner.net", nil, true, false},
	} {
		external, spoofed := senderOrigin(tc.address, tc.auth, domains)
		if external != tc.external || spoofed != tc.spoofed {
			t.Errorf("senderOrigin(%q, %+v) = %v, %v; want %v, %v", tc.address, tc.auth, external, spoofed, tc.external, tc.spoofed)
		}
	}
}

func TestImpersonationWarnings(t *testing.T) {
	domains := []string{"example.com"}
	people := []directoryEntry{{Name: "Grace Hopper", Address: "grace@example.com"}, {Name: "Ada Lovelace", Address: "ada@example.com"}}
	kinds := func(w []senderWarning) []string {
		var out []string
		for _, x := range w {
			out = append(out, x.Kind)
		}
		return out
	}
	got := impersonationWarnings(mailpkg.Address{Name: "Ada Lovelace", Address: "ada.lovelace@gmail.com"}, domains, people)
	if len(got) != 1 || got[0].Kind != "name" || got[0].Address != "ada@example.com" || got[0].Name != "Ada Lovelace" {
		t.Errorf("display name match: %+v", got)
	}
	got = impersonationWarnings(mailpkg.Address{Name: "ceo@example.com", Address: "boss@attacker.net"}, domains, people)
	if !slices.Equal(kinds(got), []string{"address-in-name"}) || got[0].Address != "ceo@example.com" {
		t.Errorf("address in name: %+v", got)
	}
	got = impersonationWarnings(mailpkg.Address{Name: "Grace Hopper", Address: "grace@examp1e.com"}, domains, people)
	if !slices.Equal(kinds(got), []string{"name", "lookalike"}) || got[1].Resembles != "example.com" || got[1].Domain != "examp1e.com" {
		t.Errorf("lookalike: %+v", got)
	}
	if got := impersonationWarnings(mailpkg.Address{Name: "Grace Hopper", Address: "Grace@example.com"}, domains, people); len(got) != 0 {
		t.Errorf("a spoofed colleague address is covered by the spoofing warning: %+v", got)
	}
	if got := impersonationWarnings(mailpkg.Address{Name: "Billing", Address: "billing@example.co"}, []string{"example.com", "example.co"}, people); len(got) != 0 {
		t.Errorf("an organization's own domain is not a look-alike of its other domain: %+v", got)
	}
	if got := impersonationWarnings(mailpkg.Address{Name: "Newsletter", Address: "news@partner.net"}, domains, people); len(got) != 0 {
		t.Errorf("ordinary sender: %+v", got)
	}
}

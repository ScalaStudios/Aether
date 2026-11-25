package api

import (
	"maps"
	"net/url"
	"strings"
	"testing"

	"github.com/scalastudios/aether/internal/config"
	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func TestQuarantineCategory(t *testing.T) {
	sym := func(names ...string) []quarantineSymbol {
		var out []quarantineSymbol
		for _, n := range names {
			out = append(out, quarantineSymbol{Name: n})
		}
		return out
	}
	cases := []struct {
		action  string
		symbols []quarantineSymbol
		want    string
	}{
		{"add header", sym("BAYES_SPAM"), "spam"},
		{"reject", sym("BAYES_SPAM"), "high_confidence_spam"},
		{"add header", sym("PHISHED_OPENPHISH"), "phishing"},
		{"reject", sym("PHISHING", "CLAM_VIRUS"), "malware"},
		{"reject", sym("CLAM_VIRUS_FAIL"), "high_confidence_spam"},
	}
	for _, c := range cases {
		if got := quarantineCategory(c.action, c.symbols); got != c.want {
			t.Errorf("quarantineCategory(%q, %v) = %q, want %q", c.action, c.symbols, got, c.want)
		}
	}
}

func TestExpandRecipientsFollowsAliasesLikePostfix(t *testing.T) {
	aliases := map[string][]string{
		"bob@example.test":      {"bob@example.test"},
		"carol@example.test":    {"carol@example.test"},
		"team@example.test":     {"bob@example.test", "friend@elsewhere.test", "nested@example.test"},
		"nested@example.test":   {"carol@example.test", "team@example.test"},
		"@example.test":         {"catchall@example.test"},
		"catchall@example.test": {"catchall@example.test"},
		"dmarc@example.test":    {"reports@aether-reports.internal"},
	}
	var lookups int
	lookup := func(keys []string) (map[string][]string, error) {
		lookups++
		found := map[string][]string{}
		for _, k := range keys {
			if d, ok := aliases[k]; ok {
				found[k] = d
			}
		}
		return found, nil
	}
	got, err := expandRecipients([]string{"Bob+news@Example.test", "team@example.test", "nobody@example.test", "dmarc@example.test", "x@other.test"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"bob@example.test": "bob+news@example.test", "carol@example.test": "team@example.test", "catchall@example.test": "nobody@example.test"}
	if !maps.Equal(got, want) {
		t.Fatalf("expandRecipients = %v, want %v", got, want)
	}
	if lookups > 4 {
		t.Errorf("expansion took %d lookups; each level should be one query", lookups)
	}
}

func TestDigestEscapesMessageFields(t *testing.T) {
	s := &Server{cfg: &config.Config{PublicURL: &url.URL{Scheme: "https", Host: "mail.example.test"}}}
	link := s.quarantineLink("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	if link != "https://mail.example.test/mail?item=22222222-2222-2222-2222-222222222222&quarantine=11111111-1111-1111-1111-111111111111" {
		t.Fatalf("link = %s", link)
	}
	text, html, err := buildDigest(digest{Title: "1 new message in quarantine", Address: "bob@example.test", Open: s.quarantineLink("11111111-1111-1111-1111-111111111111", ""), Retention: 30,
		Entries: []digestEntry{{From: `"Evil" <x@evil.test>`, Subject: `<script>alert(1)</script>`, Category: "Phishing", Received: "1 Jan 2026, 09:00 UTC", Link: link}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, "item=22222222") {
		t.Errorf("html digest not escaped or missing link: %s", html)
	}
	if kept := mailpkg.SanitizeHTML(html); !strings.Contains(kept, "background-color: #fa7e26") || !strings.Contains(kept, `href="`+strings.ReplaceAll(link, "&", "&amp;")+`"`) {
		t.Errorf("digest styling or links do not survive the webmail sanitizer: %s", kept)
	}
	if !strings.Contains(text, "<script>alert(1)</script>") || !strings.Contains(text, "Review: "+link) {
		t.Errorf("text digest = %s", text)
	}
}

func TestValidQuarantinePolicy(t *testing.T) {
	ok := quarantinePolicy{Access: maps.Clone(defaultQuarantineAccess), RetentionDays: 30, Digest: "weekly"}
	if msg := validQuarantinePolicy(&ok); msg != "" {
		t.Fatalf("default policy refused: %s", msg)
	}
	for name, mutate := range map[string]func(p *quarantinePolicy){
		"unknown category": func(p *quarantinePolicy) { delete(p.Access, "spam"); p.Access["bulk"] = "release" },
		"missing category": func(p *quarantinePolicy) { delete(p.Access, "malware") },
		"bad access":       func(p *quarantinePolicy) { p.Access["malware"] = "everyone" },
		"retention":        func(p *quarantinePolicy) { p.RetentionDays = 91 },
		"digest":           func(p *quarantinePolicy) { p.Digest = "hourly" },
	} {
		p := quarantinePolicy{Access: maps.Clone(defaultQuarantineAccess), RetentionDays: 30, Digest: "daily"}
		mutate(&p)
		if validQuarantinePolicy(&p) == "" {
			t.Errorf("%s: policy accepted", name)
		}
	}
}

func TestRoutesRegisterWithoutConflicts(t *testing.T) {
	(&Server{}).Handler()
}

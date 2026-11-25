package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scalastudios/aether/internal/config"
	mailpkg "github.com/scalastudios/aether/internal/mail"
)

func TestParseDeviceLabelAndFingerprint(t *testing.T) {
	cases := []struct {
		ua    string
		label string
	}{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "Chrome on macOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0", "Edge on Windows"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0", "Firefox on Linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1", "Safari on iPhone"},
		{"curl/8.7.1", "curl"},
		{"", "Unknown device"},
	}
	for _, tc := range cases {
		got, _ := parseDeviceLabel(tc.ua)
		if got != tc.label {
			t.Errorf("parseDeviceLabel(%q) = %q, want %q", tc.ua, got, tc.label)
		}
	}
	_, hash1 := deviceFingerprint("Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0")
	_, hash2 := deviceFingerprint("Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0")
	if hash1 != hash2 {
		t.Fatalf("minor browser version bump changed device fingerprint: %s != %s", hash1, hash2)
	}
	_, hash3 := deviceFingerprint("Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0")
	if hash1 == hash3 {
		t.Fatalf("different OS produced same device fingerprint: %s", hash1)
	}
}

func TestClientCountryRespectsTrustProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.Header.Set("CF-IPCountry", "de")

	untrusted := &Server{cfg: &config.Config{TrustProxy: false}}
	if got := untrusted.clientCountry(req); got != "" {
		t.Fatalf("untrusted proxy returned country %q", got)
	}

	trusted := &Server{cfg: &config.Config{TrustProxy: true}}
	if got := trusted.clientCountry(req); got != "DE" {
		t.Fatalf("trusted proxy returned %q, want DE", got)
	}

	req.Header.Set("CF-IPCountry", "XX")
	if got := trusted.clientCountry(req); got != "" {
		t.Fatalf("unknown country XX should be ignored, got %q", got)
	}

	req.Header.Del("CF-IPCountry")
	req.Header.Set("X-Country-Code", "jp")
	if got := trusted.clientCountry(req); got != "JP" {
		t.Fatalf("fallback header returned %q, want JP", got)
	}
}

func TestNewlyAddedExternalForwards(t *testing.T) {
	orgDomains := map[string]bool{"example.com": true, "subsidiary.example": true}
	oldRules := []mailpkg.Rule{
		{
			Name:    "Internal copy",
			Enabled: true,
			Actions: mailpkg.RuleActions{ForwardTo: []string{"team@example.com"}},
		},
		{
			Name:    "Disabled external",
			Enabled: false,
			Actions: mailpkg.RuleActions{ForwardTo: []string{"disabled@outside.test"}},
		},
		{
			Name:    "Existing external",
			Enabled: true,
			Actions: mailpkg.RuleActions{ForwardTo: []string{"known@outside.test"}},
		},
	}
	newRules := []mailpkg.Rule{
		{
			Name:    "Internal copy",
			Enabled: true,
			Actions: mailpkg.RuleActions{ForwardTo: []string{"team@example.com", "ops@subsidiary.example"}},
		},
		{
			Name:    "Disabled external",
			Enabled: true,
			Actions: mailpkg.RuleActions{ForwardTo: []string{"disabled@outside.test"}},
		},
		{
			Name:    "Existing external renamed",
			Enabled: true,
			Actions: mailpkg.RuleActions{ForwardTo: []string{"known@outside.test"}},
		},
		{
			Name:    "Exfil rule",
			Enabled: true,
			Actions: mailpkg.RuleActions{ForwardTo: []string{"Attacker@Evil.Test"}},
		},
	}

	added, ruleNames := newlyAddedExternalForwards(oldRules, newRules, orgDomains)
	wantAddrs := []string{"attacker@evil.test", "disabled@outside.test"}
	if strings.Join(added, ",") != strings.Join(wantAddrs, ",") {
		t.Fatalf("added = %v, want %v", added, wantAddrs)
	}
	wantRules := []string{"Disabled external", "Exfil rule"}
	if strings.Join(ruleNames, ",") != strings.Join(wantRules, ",") {
		t.Fatalf("ruleNames = %v, want %v", ruleNames, wantRules)
	}
}

func TestRemoveExternalForwards(t *testing.T) {
	rules := []mailpkg.Rule{
		{
			Name:    "Forward only",
			Enabled: true,
			Match:   "all",
			Conditions: []mailpkg.RuleCondition{
				{Field: "from", Contains: "@"},
			},
			Actions: mailpkg.RuleActions{ForwardTo: []string{"evil@outside.test"}, KeepCopy: true},
		},
		{
			Name:    "Move and forward",
			Enabled: true,
			Match:   "all",
			Conditions: []mailpkg.RuleCondition{
				{Field: "subject", Contains: "invoice"},
			},
			Actions: mailpkg.RuleActions{MoveTo: "Archive", ForwardTo: []string{"evil@outside.test", "ok@example.com"}, KeepCopy: true},
		},
	}

	updated, changed := removeExternalForwards(rules, []string{"EVIL@OUTSIDE.TEST"})
	if !changed {
		t.Fatal("expected rules to change")
	}
	if len(updated) != 1 {
		t.Fatalf("expected forward-only rule to be removed, got %d rules", len(updated))
	}
	if updated[0].Name != "Move and forward" {
		t.Fatalf("unexpected remaining rule: %q", updated[0].Name)
	}
	if len(updated[0].Actions.ForwardTo) != 1 || updated[0].Actions.ForwardTo[0] != "ok@example.com" {
		t.Fatalf("unexpected remaining forwards: %v", updated[0].Actions.ForwardTo)
	}
	if err := updated[0].Validate(); err != nil {
		t.Fatalf("remaining rule failed validation: %v", err)
	}
}

func TestCanUndoNotification(t *testing.T) {
	now := time.Now()
	sessA := []byte("session-a")
	sessB := []byte("session-b")

	if !canUndoNotification(secEventAppPasswordCreated, nil, nil, sessA) {
		t.Fatal("app password notification should be undoable")
	}
	if canUndoNotification(secEventAppPasswordCreated, &now, nil, sessA) {
		t.Fatal("already undone notification should not be undoable")
	}
	if canUndoNotification(secEventFailedSignIns, nil, nil, sessA) {
		t.Fatal("failed sign-ins notification should not be undoable")
	}
	if canUndoNotification(secEventNewDeviceSignIn, nil, sessA, sessA) {
		t.Fatal("current session sign-in should not be undoable from itself")
	}
	if !canUndoNotification(secEventNewDeviceSignIn, nil, sessB, sessA) {
		t.Fatal("other session sign-in should be undoable")
	}
}

func TestBuildSecurityEmailEscapesHTMLAndIncludesLinks(t *testing.T) {
	n := &securityNotification{
		ID:             "11111111-1111-1111-1111-111111111111",
		EventType:      secEventAppPasswordCreated,
		Title:          "App password created",
		Summary:        `An app password named "<script>alert(1)</script>" was created for alice@example.com.`,
		MailboxAddress: "alice@example.com",
		ActionURL:      "/settings#app-passwords",
		Details: map[string]any{
			"appPasswordName": `<script>alert(1)</script>`,
			"ip":              "203.0.113.10",
		},
		CreatedAt: time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC),
	}

	userSpec := buildSecurityEmail("https://mail.example.com", "Acme <Corp>", "user", "alice@example.com", n, "")
	if !strings.Contains(userSpec.TextBody, "https://mail.example.com/settings?alert=11111111-1111-1111-1111-111111111111#security") {
		t.Fatalf("missing review link in text body: %s", userSpec.TextBody)
	}
	if !strings.Contains(userSpec.TextBody, "https://mail.example.com/settings#app-passwords") {
		t.Fatalf("missing direct action link in text body: %s", userSpec.TextBody)
	}
	if strings.Contains(userSpec.HTMLBody, "<script>") {
		t.Fatalf("unescaped script tag in HTML body: %s", userSpec.HTMLBody)
	}
	if !strings.Contains(userSpec.HTMLBody, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("expected escaped app password name in HTML body")
	}

	reportSpec := buildSecurityEmail("https://mail.example.com", "Acme", "admin-report", "alice@example.com", n, "I never created this!")
	if !strings.Contains(reportSpec.Subject, "Security Report") {
		t.Fatalf("unexpected report subject: %q", reportSpec.Subject)
	}
	if !strings.Contains(reportSpec.TextBody, "I never created this!") {
		t.Fatalf("missing user report note in email")
	}
	if !strings.Contains(reportSpec.TextBody, "https://mail.example.com/admin") {
		t.Fatalf("missing admin link in report email")
	}
}

func TestOutgoingAutoSubmittedHeader(t *testing.T) {
	out := &mailpkg.Outgoing{
		From:          mailpkg.Address{Name: "Aether Security", Address: "security@mail.example.com"},
		To:            []mailpkg.Address{{Address: "alice@example.com"}},
		Subject:       "Security alert",
		Text:          "Alert body",
		HTML:          "<p>Alert body</p>",
		AutoSubmitted: "auto-generated",
	}
	raw, _, err := out.Build("mail.example.com")
	if err != nil {
		t.Fatal(err)
	}
	msg := string(raw)
	if !strings.Contains(msg, "Auto-Submitted: auto-generated") {
		t.Fatalf("missing Auto-Submitted header in message:\n%s", msg)
	}
	if !strings.Contains(msg, "X-Auto-Response-Suppress: All") {
		t.Fatalf("missing X-Auto-Response-Suppress header in message:\n%s", msg)
	}
}

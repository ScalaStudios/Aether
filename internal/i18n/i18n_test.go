package i18n

import (
	"strings"
	"testing"
	"time"
)

func TestCatalogParity(t *testing.T) {
	base := catalogs[DefaultLocale]
	if len(base) == 0 {
		t.Fatal("default locale catalog is empty")
	}
	for _, info := range SupportedLocales() {
		cat := catalogs[info.Code]
		for k := range base {
			if _, ok := cat[k]; !ok {
				t.Errorf("locale %q missing key %q", info.Code, k)
			}
		}
		for k := range cat {
			if _, ok := base[k]; !ok {
				t.Errorf("locale %q has extra key %q", info.Code, k)
			}
		}
	}
}

func TestNormalizeAndResolve(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"en", "en", true},
		{"en-US", "en", true},
		{"pt", "pt", true},
		{"pt-BR", "pt", true},
		{"pt_PT", "pt", true},
		{"es-419", "es", true},
		{"fr", "", false},
		{"xx-YY", "", false},
	}
	for _, tc := range cases {
		got, ok := Normalize(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Normalize(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if got := Resolve("", "fr-FR", "pt-BR", "es"); got != "pt" {
		t.Errorf("Resolve = %q, want pt", got)
	}
	if got := Resolve("", "de"); got != "en" {
		t.Errorf("Resolve fallback = %q, want en", got)
	}
}

func TestFromAcceptLanguage(t *testing.T) {
	if got := FromAcceptLanguage("fr-FR, fr;q=0.9, es-MX;q=0.8, en;q=0.7"); got != "es" {
		t.Errorf("got %q, want es", got)
	}
	if got := FromAcceptLanguage("en;q=0.5, pt-BR;q=0.9"); got != "pt" {
		t.Errorf("got %q, want pt", got)
	}
	if got := FromAcceptLanguage("pt;q=0, en;q=0.8"); got != "en" {
		t.Errorf("got %q, want en", got)
	}
	if got := FromAcceptLanguage("de-DE, fr;q=0.9"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestTranslationAndFallback(t *testing.T) {
	if got := T("pt", "email.autoReply.defaultSubject"); got != "Resposta automática" {
		t.Errorf("got %q", got)
	}
	if got := T("es", "email.invitation.subject.request", map[string]string{"title": "Reunión"}); got != "Invitación: Reunión" {
		t.Errorf("got %q", got)
	}
	if got := T("fr", "email.autoReply.defaultSubject"); got != "Automatic reply" {
		t.Errorf("fallback got %q", got)
	}
	if got := T("pt", "nonexistent.key"); got != "nonexistent.key" {
		t.Errorf("missing key got %q", got)
	}
}

func TestLocalizedDates(t *testing.T) {
	start := time.Date(2026, time.January, 5, 14, 30, 0, 0, time.UTC)
	end := time.Date(2026, time.January, 5, 15, 30, 0, 0, time.UTC)
	if got := FormatDate("en", start); got != "Monday, 5 January 2026" {
		t.Errorf("en date = %q", got)
	}
	if got := FormatDate("pt", start); got != "segunda-feira, 5 de janeiro de 2026" {
		t.Errorf("pt date = %q", got)
	}
	if got := FormatDate("es", start); got != "lunes, 5 de enero de 2026" {
		t.Errorf("es date = %q", got)
	}
	if got := FormatDateTimeRange("es", start, end); got != "lunes, 5 de enero de 2026, 14:30 – 15:30 UTC" {
		t.Errorf("es range = %q", got)
	}
}

func TestRenderEmail(t *testing.T) {
	msg := RenderEmail("pt", "quotaWarning", map[string]string{
		"name":         "Ana",
		"address":      "ana@example.com",
		"used":         "9.2 GB",
		"limit":        "10 GB",
		"percent":      "92",
		"organization": "Acme",
	})
	if !strings.Contains(msg.Subject, "92%") || !strings.Contains(msg.Text, "ana@example.com") {
		t.Errorf("unexpected quotaWarning email: %+v", msg)
	}
	digest := RenderEmail("es", "digest", map[string]string{
		"name":         "Luis",
		"period":       "Hoy",
		"summary":      "3 mensajes nuevos",
		"organization": "Acme",
	})
	if !strings.Contains(digest.Subject, "Acme") || !strings.Contains(digest.Text, "3 mensajes nuevos") {
		t.Errorf("unexpected digest email: %+v", digest)
	}
	notif := RenderEmail("en", "notification", map[string]string{
		"name":         "Sam",
		"title":        "New sign-in",
		"message":      "A new device signed in.",
		"organization": "Acme",
	})
	if notif.Subject != "New sign-in" || !strings.Contains(notif.Text, "A new device signed in.") {
		t.Errorf("unexpected notification email: %+v", notif)
	}
}

package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/scalastudios/aether/internal/config"
)

func TestBlockKeyGroupsIPv6ByPrefix(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":               "203.0.113.7",
		"::ffff:203.0.113.7":        "203.0.113.7",
		"2001:db8:1:2:aaaa::1":      "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::9%eth0": "2001:db8:1:2::/64",
		" 198.51.100.1 ":            "198.51.100.1",
	}
	for in, want := range cases {
		if got, ok := blockKey(in); !ok || got != want {
			t.Errorf("blockKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := blockKey("not-an-ip"); ok {
		t.Error("blockKey accepted an invalid address")
	}
}

func TestAuthBlockAllowlist(t *testing.T) {
	s := &Server{cfg: &config.Config{AuthBlockAllowNets: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32")}}}
	for ip, want := range map[string]bool{"10.1.2.3": true, "::ffff:10.1.2.3": true, "2001:db8::5": true, "203.0.113.7": false, "": true} {
		if got := s.authBlockExempt(ip); got != want {
			t.Errorf("authBlockExempt(%q) = %v, want %v", ip, got, want)
		}
	}
}

func TestAuthPolicyRequiresKeyAndPrivateCaller(t *testing.T) {
	s := &Server{cfg: &config.Config{DovecotAPIKey: "secret"}}
	for _, c := range []struct{ remote, key string }{{"203.0.113.7:4000", "secret"}, {"172.18.0.5:4000", "wrong"}, {"172.18.0.5:4000", ""}} {
		r := httptest.NewRequest(http.MethodPost, "/internal/auth-policy?command=allow", strings.NewReader(`{}`))
		r.RemoteAddr = c.remote
		r.Header.Set("X-API-Key", c.key)
		w := httptest.NewRecorder()
		s.authPolicy(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("remote %s key %q: status %d, want 403", c.remote, c.key, w.Code)
		}
	}
}

package api

import (
	"bufio"
	"strings"
	"testing"
)

func TestOutboundRouteValidation(t *testing.T) {
	for _, mode := range []string{"none", "may", "encrypt", "dane", "verify", "secure"} {
		route := outboundRoute{Kind: "tls", Name: "[partner.example]:2525", TLSMode: mode}
		if err := validateOutboundRoute(route, "", "", "mail.aether.test"); err != "" {
			t.Errorf("%s: %s", mode, err)
		}
	}
	for _, route := range []outboundRoute{
		{Kind: "transport", Name: "aether-reports.internal", Target: "[partner.example]:2525"},
		{Kind: "transport", Name: "partner.example", Target: "[partner.example]:25, attacker.example"},
		{Kind: "tls", Name: "partner.example", TLSMode: "none", MatchNames: []string{"partner.example"}},
		{Kind: "tls", Name: "partner.example", TLSMode: "secure", MatchNames: []string{"a.example:b.example"}},
		{Kind: "tls", Name: "partner.example", TLSMode: "dane-only"},
	} {
		if err := validateOutboundRoute(route, "", "", "mail.aether.test"); err == "" {
			t.Errorf("accepted invalid route: %+v", route)
		}
	}
	relay := outboundRoute{Kind: "relay", Name: "sender.example", Target: "[partner.example]:2525"}
	if err := validateOutboundRoute(relay, "user", "", "mail.aether.test"); err == "" {
		t.Fatal("accepted a username without a password")
	}
}

func TestOutboundSASLRequestFraming(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("16:key user@example,3:key,"))
	for _, want := range []string{"key user@example", "key"} {
		got, err := readOutboundSASLRequest(reader)
		if err != nil || got != want {
			t.Fatalf("got %q, %v, want %q", got, err, want)
		}
	}
	for _, invalid := range []string{"-5:key,", "10000:key,", "3:key.", "5:key,"} {
		if _, err := readOutboundSASLRequest(bufio.NewReader(strings.NewReader(invalid))); err == nil {
			t.Errorf("accepted invalid frame %q", invalid)
		}
	}
}

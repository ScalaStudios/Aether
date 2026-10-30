package api

import (
	"context"
	"net"
	"testing"

	"github.com/scalastudios/aether/internal/config"
)

type fakeDNS struct {
	txt   map[string][]string
	hosts map[string][]string
	mx    map[string][]*net.MX
}

func dnsNotFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, dnsNotFound(name)
}

func (f fakeDNS) LookupHost(_ context.Context, name string) ([]string, error) {
	if v, ok := f.hosts[name]; ok {
		return v, nil
	}
	return nil, dnsNotFound(name)
}

func (f fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if v, ok := f.mx[name]; ok {
		return v, nil
	}
	return nil, dnsNotFound(name)
}

func (f fakeDNS) LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error) {
	return "", nil, dnsNotFound("srv")
}

func TestSPFStatus(t *testing.T) {
	dns := fakeDNS{
		hosts: map[string][]string{"mail.lunarlabs.dev": {"31.77.212.29"}, "mx.example.org": {"31.77.212.29"}},
		mx:    map[string][]*net.MX{"mx.test": {{Host: "mx.example.org.", Pref: 10}}},
		txt: map[string][]string{
			"lunarlabs.dev":              {"v=spf1 include:mail.lunarlabs.dev include:spf.protection.outlook.com ~all"},
			"mail.lunarlabs.dev":         {"v=spf1 ip4:31.77.212.29 -all"},
			"spf.protection.outlook.com": {"v=spf1 ip4:40.92.0.0/15 -all"},
			"mx.test":                    {"v=spf1 mx -all"},
			"cidr.test":                  {"v=spf1 ip4:31.77.212.0/24 -all"},
			"redirect.test":              {"v=spf1 redirect=mail.lunarlabs.dev"},
			"a.test":                     {"v=spf1 a:mx.example.org/24 -all"},
			"soft.test":                  {"v=spf1 ip4:10.0.0.1 ~all"},
			"fail.test":                  {"v=spf1 ip4:10.0.0.1 -all"},
			"two.test":                   {"v=spf1 -all", "v=spf1 mx -all"},
			"loop.test":                  {"v=spf1 include:loop.test -all"},
			"unknown.test":               {"v=spf1 include:missing.test -all"},
		},
	}
	s := &Server{cfg: &config.Config{MailHostname: "mail.lunarlabs.dev"}, resolve: dns}
	cases := map[string]string{
		"lunarlabs.dev": "ok",
		"mx.test":       "ok",
		"cidr.test":     "ok",
		"redirect.test": "ok",
		"a.test":        "ok",
		"soft.test":     "warning",
		"fail.test":     "mismatch",
		"two.test":      "warning",
		"loop.test":     "warning",
		"unknown.test":  "warning",
	}
	for domain, want := range cases {
		if got := s.spfStatus(context.Background(), domain); got != want {
			t.Errorf("%s = %s, want %s", domain, got, want)
		}
	}
}

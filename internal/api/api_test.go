package api

import "testing"

func TestSafeReturnToRejectsOffsiteTargets(t *testing.T) {
	cases := map[string]string{
		"/mail?folder=Sent":    "/mail?folder=Sent",
		"":                     "/mail",
		"//evil.example/x":     "/mail",
		"/\\evil.example":      "/mail",
		"https://evil.example": "/mail",
		"javascript:alert(1)":  "/mail",
		"mail":                 "/mail",
	}
	for in, want := range cases {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecordMatches(t *testing.T) {
	dkim := &dnsRecord{Purpose: "dkim", Value: "v=DKIM1; k=rsa; p=ABC123"}
	spf := &dnsRecord{Purpose: "spf"}
	dmarc := &dnsRecord{Purpose: "dmarc"}
	checks := []struct {
		rec   *dnsRecord
		found string
		want  bool
	}{
		{dkim, "v=DKIM1; k=rsa; p=ABC123", true},
		{dkim, "v=DKIM1; k=rsa; p=OTHER", false},
		{spf, "v=spf1 mx -all", true},
		{spf, "v=spf1 a:mail.example.com ~all", true},
		{spf, "v=spf1 include:_spf.google.com -all", false},
		{dmarc, "v=DMARC1; p=none", true},
		{dmarc, "v=spf1 -all", false},
	}
	for _, c := range checks {
		if got := recordMatches(c.rec, c.found, "mail.example.com"); got != c.want {
			t.Errorf("%s %q = %v, want %v", c.rec.Purpose, c.found, got, c.want)
		}
	}
}

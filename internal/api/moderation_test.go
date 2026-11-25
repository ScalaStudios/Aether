package api

import "testing"

func TestListRecipient(t *testing.T) {
	id := "0f8c2a1e-4b6d-4e3a-9c1f-2a7b8d9e0f12"
	for rcpt, want := range map[string]string{
		"list-" + id + "@aether-reports.internal":     id,
		"LIST-" + id + "+tag@Aether-Reports.Internal": id,
		"reports@aether-reports.internal":             "",
		"list-" + id + "@example.com":                 "",
		"list-not-a-uuid@aether-reports.internal":     "",
		"list-" + id + "x@aether-reports.internal":    "",
	} {
		got, ok := listRecipient(rcpt)
		if ok != (want != "") || got != want && ok {
			t.Errorf("listRecipient(%q) = %q, %v; want %q", rcpt, got, ok, want)
		}
	}
}

func TestSRSOriginal(t *testing.T) {
	for in, want := range map[string]string{
		"SRS0=AbCd=7Q=Partner.Example=CEO@mail.example.com":                  "ceo@partner.example",
		"srs0=abcd=7q=partner.example=a=b@mail.example.com":                  "a=b@partner.example",
		"SRS1=xx=orig.example==AbCd=7Q=partner.example=ceo@mail.example.com": "srs1=xx=orig.example==abcd=7q=partner.example=ceo@mail.example.com",
		"SRS0=broken@mail.example.com":                                       "srs0=broken@mail.example.com",
		"Alice@Example.com":                                                  "alice@example.com",
		"":                                                                   "",
	} {
		if got := srsOriginal(in); got != want {
			t.Errorf("srsOriginal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsExempt(t *testing.T) {
	exempt := []string{"owner@example.com", "@partner.example"}
	for sender, want := range map[string]bool{
		"owner@example.com":   true,
		"ceo@partner.example": true,
		"other@example.com":   false,
		"partner.example":     false,
		"":                    false,
	} {
		if got := isExempt(sender, exempt); got != want {
			t.Errorf("isExempt(%q) = %v, want %v", sender, got, want)
		}
	}
}

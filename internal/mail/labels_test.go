package mail

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestKeywordMatchesThunderbirdTagKeys(t *testing.T) {
	cases := map[string]string{
		"Invoice":  "invoice",
		"Client X": "client_x",
		"Hello!!!": "hello",
		"  ":       "label",
		"Seen":     "tag_seen",
		"123abc":   "n_123abc",
		"A/B.C":    "a_b_c",
		"日本語":      "label",
	}
	for name, want := range cases {
		if got := Keyword(name); got != want {
			t.Errorf("Keyword(%q) = %q, want %q", name, got, want)
		}
	}
	taken := map[string]bool{"invoice": true, "invoice_2": true}
	if got := NextKeyword("Invoice", func(keyword string) bool { return taken[keyword] }); got != "invoice_3" {
		t.Errorf("NextKeyword = %q, want invoice_3", got)
	}
}

func TestDiffLabelFlagsKeepsUnknownKeywordsAndAppleColor(t *testing.T) {
	catalog := []LabelRef{
		{Keyword: "invoice", Color: "red", Position: 0},
		{Keyword: "client_x", Color: "blue", Position: 1},
	}
	add, remove := DiffLabelFlags([]string{"$MailFlagBit0", "$label1"}, catalog, []string{"invoice"}, nil)
	if !slicesEqual(add, []string{"invoice"}) || len(remove) != 0 {
		t.Fatalf("add red label: add %v remove %v", add, remove)
	}
	current := []string{"invoice", "$MailFlagBit0", "$label1", "elsewhere"}
	add, remove = DiffLabelFlags(current, catalog, []string{"client_x"}, nil)
	if !slicesEqual(add, []string{"client_x"}) || len(remove) != 0 {
		t.Fatalf("second label keeps the first color: add %v remove %v", add, remove)
	}
	add, remove = DiffLabelFlags(append(current, "client_x"), catalog, nil, []string{"invoice"})
	if !slicesEqual(add, []string{"$MailFlagBit2"}) || !slicesEqual(remove, []string{"invoice"}) {
		t.Fatalf("drop invoice: add %v remove %v", add, remove)
	}
	add, remove = DiffLabelFlags([]string{"$MailFlagBit0", "$MailFlagBit1"}, catalog, nil, nil)
	if len(add) != 0 || len(remove) != 0 {
		t.Fatalf("apple-only color changed: add %v remove %v", add, remove)
	}
	if FlagColor([]string{"$MailFlagBit0", "$MailFlagBit2"}) != "blue" || FlagColor([]string{"$MailFlagBit0"}) != "red" {
		t.Fatal("flag color")
	}
}

func TestListCriteriaIncludeKeywords(t *testing.T) {
	since := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	got := ListOptions{Keywords: []string{"invoice"}, Since: since}.criteria()
	want := &imap.SearchCriteria{Flag: []imap.Flag{"invoice"}, Since: since}
	if len(got.Flag) != 1 || got.Flag[0] != "invoice" || !got.Since.Equal(since) {
		t.Fatalf("criteria = %+v, want %+v", got, want)
	}
}

func TestCompileSieveAddsLabelKeywordsAndAppleColor(t *testing.T) {
	rule := Rule{Name: "invoices", Enabled: true, Match: "all", Conditions: []RuleCondition{{Field: "from", Contains: "billing"}}, Actions: RuleActions{Labels: []string{"invoice", "client_x"}}}
	if err := rule.Validate(); err != nil {
		t.Fatal(err)
	}
	script := CompileSieve(WithLabelColors([]Rule{rule}, []LabelRef{
		{Keyword: "invoice", Color: "red", Position: 0},
		{Keyword: "client_x", Color: "blue", Position: 1},
	}), AutoReply{}, "me@example.test", ForwardPolicy{})
	want := `addflag ["invoice", "client_x", "$MailFlagBit0"];`
	if !strings.Contains(script, want) {
		t.Fatalf("script missing %s:\n%s", want, script)
	}
	if (Rule{Name: "x", Match: "all", Conditions: []RuleCondition{{Field: "subject", Contains: "a"}}, Actions: RuleActions{Labels: []string{"$label1"}}}).Validate() == nil {
		t.Fatal("system keyword accepted as a label")
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func slicesContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

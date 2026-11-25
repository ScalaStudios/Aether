package mail

import (
	"reflect"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestParseSearchQuery(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want ParsedQuery
	}{
		{"", ParsedQuery{}},
		{`quarterly "net revenue"  report`, ParsedQuery{Text: []string{"quarterly", "net revenue", "report"}}},
		{`From:amy to:"Ops Team" has:attachment filename:report.pdf`, ParsedQuery{From: []string{"amy"}, To: []string{"Ops Team"}, Filenames: []string{"report.pdf"}, HasAttachments: true}},
		{`has:star subject:hello from: 10:30 "unterminated`, ParsedQuery{Text: []string{"has:star", "subject:hello", "10:30", "unterminated"}}},
	} {
		if got := ParseSearchQuery(c.raw); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseSearchQuery(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}

func TestMatchesAttachments(t *testing.T) {
	text := &imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"}
	pdf := &imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf", Extended: &imap.BodyStructureSinglePartExt{
		Disposition: &imap.BodyStructureDisposition{Value: "attachment", Params: map[string]string{"filename": "Q3-Report.pdf"}},
	}}
	mixed := &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{text, pdf}}
	for _, c := range []struct {
		bs        imap.BodyStructure
		filenames []string
		want      bool
	}{
		{nil, nil, false},
		{text, nil, false},
		{mixed, nil, true},
		{mixed, []string{"report"}, true},
		{mixed, []string{"q3", ".PDF"}, true},
		{mixed, []string{"invoice"}, false},
	} {
		if got := matchesAttachments(c.bs, c.filenames); got != c.want {
			t.Errorf("matchesAttachments(%v, %v) = %v, want %v", c.bs, c.filenames, got, c.want)
		}
	}
}

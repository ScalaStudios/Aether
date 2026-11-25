package api

import (
	"bytes"
	"strings"
	"testing"
)

func TestNextQuotaWarning(t *testing.T) {
	levels := []int16{80, 95}
	for _, c := range []struct {
		percent    int64
		warned     int16
		next       int16
		send       bool
		thresholds []int16
	}{
		{50, 0, 0, false, levels},
		{80, 0, 80, true, levels},
		{97, 0, 95, true, levels},
		{96, 80, 95, true, levels},
		{120, 95, 95, false, levels},
		{79, 80, 80, false, levels},
		{74, 80, 0, false, levels},
		{92, 95, 95, false, levels},
		{89, 95, 80, false, levels},
		{99, 0, 0, false, nil},
		{70, 80, 60, false, []int16{60}},
		{50, 80, 0, false, []int16{60}},
	} {
		next, send := nextQuotaWarning(c.percent, c.thresholds, c.warned)
		if next != c.next || send != c.send {
			t.Errorf("nextQuotaWarning(%d, %v, %d) = %d, %v; want %d, %v", c.percent, c.thresholds, c.warned, next, send, c.next, c.send)
		}
	}
}

func TestQuotaNoticeRendersEscapedCopy(t *testing.T) {
	n := quotaNotice{
		quotaMailbox: quotaMailbox{Address: "jane@example.com", Kind: "user", Used: 4404019, Quota: 5242880, OrgName: "Acme <Labs>"},
		Percent:      84, Level: 80, Critical: false,
		Folders: []folderUsage{{Name: "Projects/<2024>", Size: "2.1 MB"}},
	}
	c := n.forMailbox("https://mail.example.com")
	if c.Subject != "jane@example.com is 84% full" || c.Heading != "Your mailbox is 84% full" || c.URL != "https://mail.example.com/settings#storage" {
		t.Fatalf("unexpected copy: %+v", c)
	}
	var html bytes.Buffer
	if err := quotaMailHTML.Execute(&html, c); err != nil {
		t.Fatal(err)
	}
	out := html.String()
	for _, want := range []string{"background-color:#e0a21b", `width="84%"`, "Projects/&lt;2024&gt;", "Acme &lt;Labs&gt;", "4.2 MB of 5.0 MB"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered warning is missing %q", want)
		}
	}
	if strings.Contains(out, "ZgotmplZ") {
		t.Error("a template value was filtered as unsafe")
	}
	full := quotaNotice{quotaMailbox: n.quotaMailbox, Percent: 103, Level: 95, Critical: true}.forAdmin("https://mail.example.com")
	if full.Subject != "jane@example.com is full" || full.Color != "#c92438" || full.Fill != 100 || !strings.Contains(full.text(), "Open Administration: https://mail.example.com/admin") {
		t.Fatalf("unexpected full copy: %+v", full)
	}
}

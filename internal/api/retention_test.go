package api

import (
	"reflect"
	"testing"
)

func days(n int) *int { return &n }

func TestResolveRetention(t *testing.T) {
	cases := []struct {
		name string
		rows []retentionRow
		want mailboxRetention
	}{
		{"built-in defaults", nil, mailboxRetention{Folders: []retentionRule{{"Junk", 30}, {"Trash", 30}}}},
		{
			"mailbox overrides organization regardless of order",
			[]retentionRow{
				{Mailbox: true, Action: "delete", Folder: "INBOX", Days: days(90)},
				{Action: "delete", Folder: "INBOX", Days: days(365)},
				{Action: "delete", Folder: "Trash", Days: days(7)},
				{Mailbox: true, Action: "delete", Folder: "Junk"},
			},
			mailboxRetention{Folders: []retentionRule{{"INBOX", 90}, {"Trash", 7}}},
		},
		{
			"longest keep wins",
			[]retentionRow{
				{Mailbox: true, Action: "keep", Days: days(30)},
				{Action: "keep", Days: days(2557)},
				{Action: "delete", Folder: "Trash"},
			},
			mailboxRetention{KeepDays: 2557, Folders: []retentionRule{{"Junk", 30}}},
		},
	}
	for _, c := range cases {
		if got := resolveRetention(c.rows); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestExpungeQuery(t *testing.T) {
	cases := []struct {
		folder     string
		days, keep int
		want       []string
	}{
		{"Trash", 30, 0, []string{"mailbox", "Trash", "savedbefore", "30d"}},
		{"Junk", 30, 365, []string{"mailbox", "Junk", "savedbefore", "30d", "before", "365d"}},
		{"INBOX", 90, 0, []string{"mailbox", "INBOX", "before", "90d"}},
		{"Clients/Acme", 90, 365, []string{"mailbox", "Clients/Acme", "before", "365d"}},
		{"Sent", 400, 365, []string{"mailbox", "Sent", "before", "400d"}},
	}
	for _, c := range cases {
		if got := expungeQuery(c.folder, c.days, c.keep); !reflect.DeepEqual(got, c.want) {
			t.Errorf("expungeQuery(%q, %d, %d) = %v, want %v", c.folder, c.days, c.keep, got, c.want)
		}
	}
}

func TestRetentionFolder(t *testing.T) {
	for in, want := range map[string]string{"inbox": "INBOX", " Sent ": "Sent", "Clients/Acme": "Clients/Acme"} {
		if got, ok := retentionFolder(in); !ok || got != want {
			t.Errorf("retentionFolder(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", " ", "Shared/bob@example.com/INBOX", "Shared", "a*", "50%", "/Sent", "Sent/", "a\x00b"} {
		if _, ok := retentionFolder(in); ok {
			t.Errorf("retentionFolder(%q) accepted", in)
		}
	}
}

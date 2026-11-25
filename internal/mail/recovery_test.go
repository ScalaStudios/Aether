package mail

import (
	"testing"
	"time"
)

func TestNewestExpunged(t *testing.T) {
	rows := []expungedRow{
		{Mailbox: ".EXPUNGED/INBOX", UID: "3", Saved: "1795376100"},
		{Mailbox: ".EXPUNGED/Work/Clients", UID: "7", Saved: "1795376200"},
		{Mailbox: ".EXPUNGED/INBOX", UID: "4", Saved: "1795376100"},
		{Mailbox: ".EXPUNGED/Trash", UID: "1", Saved: "1795376000"},
		{Mailbox: "INBOX", UID: "9", Saved: "1795376300"},
		{Mailbox: ".EXPUNGED/", UID: "2", Saved: "1795376300"},
		{Mailbox: ".EXPUNGED/INBOX", UID: "x", Saved: "1795376300"},
	}
	got := newestExpunged(rows, 3)
	want := []struct {
		folder string
		uid    uint32
	}{{"Work/Clients", 7}, {"INBOX", 4}, {"INBOX", 3}}
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Folder != w.folder || got[i].UID != w.uid {
			t.Errorf("item %d = %s/%d, want %s/%d", i, got[i].Folder, got[i].UID, w.folder, w.uid)
		}
	}
	if !got[0].DeletedAt.Equal(time.Unix(1795376200, 0)) {
		t.Errorf("deletedAt = %v", got[0].DeletedAt)
	}
	if n := len(newestExpunged(rows, 100)); n != 4 {
		t.Errorf("without a limit got %d items, want 4", n)
	}
}

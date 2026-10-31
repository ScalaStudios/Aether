//go:build integration

package integration

import (
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func imapOriginal(t *testing.T, c *imapclient.Client, folder, subject string) *imapclient.FetchMessageBuffer {
	t.Helper()
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("select %s: %v", folder, err)
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: subject}}}, nil).Wait()
	if err != nil || len(data.AllUIDs()) != 1 {
		t.Fatalf("%d messages in %s match %q, want 1 (%v)", len(data.AllUIDs()), folder, subject, err)
	}
	msgs, err := c.Fetch(imap.UIDSetNum(data.AllUIDs()...), &imap.FetchOptions{Flags: true, InternalDate: true}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fetch %q: %v", subject, err)
	}
	return msgs[0]
}

func TestMoveAndCopyAcrossMailboxes(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	var shared idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "hello", "displayName": "Hello"}, &shared, 201)
	delegates := "/mailboxes/" + shared.ID + "/delegates"
	w.admin.must("POST", delegates, map[string]any{"email": alice, "sendAs": true}, nil, 200)
	hello, err := imapLogin(t, "hello@"+w.domain, createAppPassword(t, w.alice, shared.ID))
	if err != nil {
		t.Fatalf("shared mailbox imap login: %v", err)
	}
	defer hello.Close()
	mine, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer mine.Close()

	suffix := randomSuffix()
	take, file, cp := "take "+suffix, "file "+suffix, "copy "+suffix
	received := time.Date(2025, 3, 14, 15, 9, 26, 0, time.UTC)
	for _, subject := range []string{take, file, cp} {
		imapAppend(t, hello, "INBOX", rfc822("customer@external.example", "hello@"+w.domain, subject, "question"), received, imap.FlagFlagged, "$Customer")
	}
	sharedMessages := "/mailboxes/" + shared.ID + "/messages"

	w.alice.must("POST", sharedMessages+"/"+itoaUID(findInAPI(w.alice, shared.ID, "INBOX", take))+"/move", map[string]any{"mailboxId": w.aliceMailbox, "to": "INBOX"}, nil, 204)
	if findInAPI(w.alice, shared.ID, "INBOX", take) != 0 {
		t.Fatal("the moved message is still in the shared mailbox")
	}
	got := imapOriginal(t, mine, "INBOX", take)
	if !got.InternalDate.Equal(received) || !slices.Contains(got.Flags, imap.FlagFlagged) || !slices.Contains(got.Flags, "$Customer") {
		t.Fatalf("the moved message lost its date or flags: %v %v", got.InternalDate, got.Flags)
	}

	w.alice.must("POST", sharedMessages+"/"+itoaUID(findInAPI(w.alice, shared.ID, "INBOX", cp))+"/copy", map[string]any{"mailboxId": w.aliceMailbox, "to": "Archive"}, nil, 204)
	if findInAPI(w.alice, shared.ID, "INBOX", cp) == 0 || findInAPI(w.alice, w.aliceMailbox, "Archive", cp) == 0 {
		t.Fatal("a copy should leave the message in both mailboxes")
	}
	w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/messages/move", map[string]any{
		"uids": []uint32{findInAPI(w.alice, w.aliceMailbox, "INBOX", take)}, "mailboxId": shared.ID, "to": "Archive",
	}, nil, 204)
	if findInAPI(w.alice, shared.ID, "Archive", take) == 0 {
		t.Fatal("the bulk move back into the shared Archive failed")
	}
	w.alice.must("POST", sharedMessages+"/move?folder=INBOX", map[string]any{
		"uids": []uint32{findInAPI(w.alice, shared.ID, "INBOX", file), findInAPI(w.alice, shared.ID, "INBOX", cp)}, "to": "Archive",
	}, nil, 204)
	if findInAPI(w.alice, shared.ID, "INBOX", file) != 0 || findInAPI(w.alice, shared.ID, "Archive", cp) == 0 {
		t.Fatal("the bulk move within the shared mailbox failed")
	}

	var events []struct {
		Action     string  `json:"action"`
		TargetID   string  `json:"targetId"`
		ActorEmail *string `json:"actorEmail"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &events, 200)
	seen := map[string]bool{}
	for _, e := range events {
		if e.ActorEmail != nil && *e.ActorEmail == alice {
			seen[e.Action+" "+e.TargetID] = true
		}
	}
	for _, want := range []string{"mailbox.messages.move " + shared.ID, "mailbox.messages.copy " + shared.ID, "mailbox.messages.move " + w.aliceMailbox} {
		if !seen[want] {
			t.Errorf("audit log is missing %q: %v", want, seen)
		}
	}

	if code := w.bob.do("POST", "/mailboxes/"+w.bobMailbox+"/messages/move", map[string]any{"uids": []uint32{1}, "mailboxId": shared.ID, "to": "INBOX"}, nil); code != 422 {
		t.Errorf("moving into a mailbox without access returned %d, want 422", code)
	}
	w.admin.must("POST", delegates, map[string]string{"email": alice, "access": "read"}, nil, 200)
	archived := itoaUID(findInAPI(w.alice, shared.ID, "Archive", take))
	for _, c := range []struct {
		path string
		body any
	}{
		{sharedMessages + "/" + archived + "/move?folder=Archive", map[string]any{"mailboxId": w.aliceMailbox, "to": "INBOX"}},
		{"/mailboxes/" + w.aliceMailbox + "/messages/copy", map[string]any{"uids": []uint32{1}, "mailboxId": shared.ID, "to": "INBOX"}},
		{"/mailboxes/" + shared.ID + "/folders", map[string]string{"name": "Nope"}},
		{"/mailboxes/" + shared.ID + "/folders/mark-read?folder=INBOX", nil},
	} {
		if code := w.alice.do("POST", c.path, c.body, nil); code != 403 {
			t.Errorf("POST %s with read-only access returned %d, want 403", c.path, code)
		}
	}
}

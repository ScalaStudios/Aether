//go:build integration

package integration

import (
	"strconv"
	"testing"
)

func TestSharedMailboxDelegation(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	support := "support@" + w.domain
	var shared idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "support", "displayName": "Support"}, &shared, 201)
	delegates := "/mailboxes/" + shared.ID + "/delegates"

	if code := w.admin.do("POST", delegates, map[string]string{"email": "stranger@elsewhere.test"}, nil); code != 422 {
		t.Fatalf("delegating to an outsider returned %d, want 422", code)
	}
	if code := w.bob.do("POST", delegates, map[string]string{"email": bob}, nil); code != 403 && code != 404 {
		t.Fatalf("a member could delegate to themselves: %d", code)
	}
	var aliceDelegate struct{ UserID string }
	w.admin.must("POST", delegates, map[string]string{"email": alice}, &aliceDelegate, 200)

	var mine []struct{ ID, Address string }
	w.alice.must("GET", "/me/mailboxes", nil, &mine, 200)
	found := false
	for _, m := range mine {
		found = found || m.ID == shared.ID
	}
	if !found {
		t.Fatalf("alice does not see the shared mailbox: %+v", mine)
	}
	if code := w.bob.do("GET", "/mailboxes/"+shared.ID+"/messages?folder=INBOX", nil, nil); code != 404 {
		t.Fatalf("bob could read the shared mailbox: %d", code)
	}

	subject := "from support " + randomSuffix()
	w.alice.must("POST", "/mailboxes/"+shared.ID+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "shared reply"}, nil, 202)
	waitFor(t, "bob to receive mail sent as support", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })
	var page messagePage
	w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages?folder=INBOX", nil, &page, 200)
	for _, m := range page.Messages {
		if m.Subject == subject && (len(m.From) != 1 || m.From[0].Address != support) {
			t.Fatalf("message was not sent as %s: %+v", support, m.From)
		}
	}

	pass := createAppPassword(t, w.alice, shared.ID)
	ic, err := imapLogin(t, support, pass)
	if err != nil {
		t.Fatalf("delegate app password IMAP login: %v", err)
	}
	ic.Close()

	w.admin.must("DELETE", delegates+"/"+aliceDelegate.UserID, nil, nil, 204)
	if code := w.alice.do("GET", "/mailboxes/"+shared.ID+"/messages?folder=INBOX", nil, nil); code != 404 {
		t.Fatalf("removed delegate can still read the mailbox: %d", code)
	}
	if ic, err := imapLogin(t, support, pass); err == nil {
		ic.Close()
		t.Fatal("removed delegate's app password still works over IMAP")
	}
}

func TestReadOnlyDelegate(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	info := "info@" + w.domain
	var shared idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "info", "displayName": "Info"}, &shared, 201)
	delegates := "/mailboxes/" + shared.ID + "/delegates"
	base := "/mailboxes/" + shared.ID

	var full struct {
		UserID string
		Access string
	}
	w.admin.must("POST", delegates, map[string]string{"email": alice}, &full, 200)
	if full.Access != "full" {
		t.Fatalf("delegates default to full access, got %q", full.Access)
	}
	pass := createAppPassword(t, w.alice, shared.ID)
	subject := "for info " + randomSuffix()
	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{"to": []map[string]string{{"address": info}}, "subject": subject, "text": "hello"}, nil, 202)
	var uid uint32
	waitFor(t, "info to receive mail", func() bool { uid = findInAPI(w.alice, shared.ID, "INBOX", subject); return uid != 0 })

	var readOnly struct{ Access string }
	w.admin.must("POST", delegates, map[string]string{"email": alice, "access": "read"}, &readOnly, 200)
	if readOnly.Access != "read" {
		t.Fatalf("access not changed to read: %+v", readOnly)
	}
	var mine []struct{ ID, Access string }
	w.alice.must("GET", "/me/mailboxes", nil, &mine, 200)
	for _, m := range mine {
		if m.ID == shared.ID && m.Access != "read" {
			t.Fatalf("mailbox list should report read access: %+v", m)
		}
	}
	message := base + "/messages/" + itoaUID(uid)
	w.alice.must("GET", message, nil, nil, 200)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", message, map[string]bool{"seen": true}},
		{"DELETE", message, nil},
		{"POST", base + "/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": "nope", "text": "nope"}},
	} {
		if code := w.alice.do(c.method, c.path, c.body, nil); code != 403 {
			t.Fatalf("%s %s by a read-only delegate returned %d, want 403", c.method, c.path, code)
		}
	}
	if code := w.alice.do("POST", "/me/app-passwords", map[string]string{"name": "phone", "mailboxId": shared.ID}, nil); code != 422 {
		t.Fatalf("read-only delegates must not get app passwords, got %d", code)
	}
	if ic, err := imapLogin(t, info, pass); err == nil {
		ic.Close()
		t.Fatal("app password made with full access still works after the downgrade")
	}
}

func itoaUID(uid uint32) string {
	return strconv.FormatUint(uint64(uid), 10)
}

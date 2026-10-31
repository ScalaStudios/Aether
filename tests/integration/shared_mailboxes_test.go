//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/jackc/pgx/v5"
)

type sharedMailbox struct {
	ID          string  `json:"id"`
	Address     string  `json:"address"`
	Kind        string  `json:"kind"`
	OwnerUserID *string `json:"ownerUserId"`
}

func createSharedMailbox(t *testing.T, w *world) sharedMailbox {
	t.Helper()
	var m sharedMailbox
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "support", "displayName": "Support", "kind": "shared"}, &m, 201)
	return m
}

type delegateView struct {
	UserID       string `json:"userId"`
	Access       string `json:"access"`
	SendAs       bool   `json:"sendAs"`
	SendOnBehalf bool   `json:"sendOnBehalf"`
}

func setDelegate(t *testing.T, c *apiClient, mailboxID string, body map[string]any) delegateView {
	t.Helper()
	var d delegateView
	c.must("POST", "/mailboxes/"+mailboxID+"/delegates", body, &d, 200)
	return d
}

func errorCode(t *testing.T, c *apiClient, method, path string, body any, want int) string {
	t.Helper()
	raw, _ := json.Marshal(body)
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	c.mustRaw(method, path, "application/json", string(raw), &e, want)
	return e.Error.Code
}

func imapNames(t *testing.T, c *imapclient.Client) []string {
	t.Helper()
	list, err := c.List("", "*", nil).Collect()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var names []string
	for _, l := range list {
		names = append(names, l.Mailbox)
	}
	return names
}

func tryAppend(c *imapclient.Client, folder, raw string) error {
	cmd := c.Append(folder, int64(len(raw)), nil)
	cmd.Write([]byte(raw))
	cmd.Close()
	_, err := cmd.Wait()
	return err
}

func waitKicked(t *testing.T, c *imapclient.Client, since time.Time) {
	t.Helper()
	for {
		if _, err := c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect(); err != nil {
			return
		}
		if time.Since(since) > 2*time.Second {
			t.Fatal("the open IMAP session still reads the shared mailbox 2 s after losing access")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func senderHeader(t *testing.T, c *imapclient.Client, subject, from string) string {
	t.Helper()
	var raw []byte
	waitFor(t, "a message with subject "+subject, func() bool { raw = imapFindSubject(t, c, "INBOX", subject); return raw != nil })
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", subject, err)
	}
	if got, err := mail.ParseAddress(msg.Header.Get("From")); err != nil || got.Address != from {
		t.Fatalf("From = %q, want %s", msg.Header.Get("From"), from)
	}
	return msg.Header.Get("Sender")
}

func testDB(t *testing.T) *pgx.Conn {
	t.Helper()
	db, err := pgx.Connect(context.Background(), env(t, "AETHER_TEST_DATABASE_URL", ""))
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}

func gauge(t *testing.T, name string) float64 {
	t.Helper()
	resp, err := http.Get(env(t, "AETHER_TEST_METRICS", "http://api:9090/metrics"))
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, name+" "); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				t.Fatalf("metrics: parse %q: %v", line, err)
			}
			return f
		}
	}
	t.Fatalf("metrics: %s is not exported", name)
	return 0
}

func TestSharedMailboxHasNoSignIn(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	shared := createSharedMailbox(t, w)
	if shared.Kind != "shared" || shared.OwnerUserID != nil {
		t.Fatalf("created %+v, want an ownerless shared mailbox", shared)
	}
	create := "/organizations/" + w.orgID + "/mailboxes"
	if code := errorCode(t, w.admin, "POST", create, map[string]any{"domainId": w.domainID, "localPart": "help", "kind": "shared", "password": "a-long-enough-password"}, 422); code != "shared_no_password" {
		t.Fatalf("shared mailbox with a password: %q, want shared_no_password", code)
	}
	if code := errorCode(t, w.admin, "POST", create, map[string]any{"domainId": w.domainID, "localPart": "alice", "kind": "shared"}, 409); code != "address_is_account" {
		t.Fatalf("shared mailbox on an account's address: %q, want address_is_account", code)
	}
	if code := newClient(t).do("POST", "/auth/login", map[string]string{"email": shared.Address, "password": "any-password-at-all"}, nil); code != 401 {
		t.Fatalf("password sign-in as the shared mailbox returned %d, want 401", code)
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full", "sendAs": true})
	if code := w.alice.do("POST", "/me/app-passwords", map[string]string{"name": "phone", "mailboxId": shared.ID}, nil); code != 422 {
		t.Fatalf("app password for a shared mailbox returned %d, want 422", code)
	}
	if code := w.alice.do("POST", "/me/apple-profile", map[string]string{"mailboxId": shared.ID}, nil); code != 422 {
		t.Fatalf("Apple profile for a shared mailbox returned %d, want 422", code)
	}

	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider idOnly
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret, "autoProvision": true,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)
	f.subject, f.email = "halo-"+randomSuffix(), alice
	oidcLogin(t, f, slug)
	token := f.mint(f.subject, f.clientID)
	if err := imapAuthenticate(t, xoauth2{alice, token}); err != nil {
		t.Fatalf("alice's token for her own mailbox: %v", err)
	}
	if err := imapAuthenticate(t, xoauth2{shared.Address, token}); err == nil {
		t.Fatal("IMAP accepted alice's access token as the shared mailbox")
	}
}

func TestSharedMailboxMembersSeeItOverIMAP(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "read"})
	subject := "for support " + randomSuffix()
	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{"to": []map[string]string{{"address": shared.Address}}, "subject": subject, "text": "hello"}, nil, 202)
	waitFor(t, "the shared mailbox to receive mail", func() bool { return findInAPI(w.alice, shared.ID, "INBOX", subject) != 0 })

	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer ic.Close()
	if !slices.Contains(imapNames(t, ic), inbox) {
		t.Fatalf("a read member does not see %s", inbox)
	}
	if _, err := ic.Select(inbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("read member EXAMINE: %v", err)
	}
	if _, err := ic.Select(inbox, nil).Wait(); err != nil {
		t.Fatalf("read member SELECT: %v", err)
	}
	ic.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close()
	var page messagePage
	w.alice.must("GET", "/mailboxes/"+shared.ID+"/messages?folder=INBOX", nil, &page, 200)
	for _, m := range page.Messages {
		if m.Subject == subject && m.Seen {
			t.Fatal("a read member could set \\Seen")
		}
	}
	if err := tryAppend(ic, inbox, rfc822(alice, shared.Address, "read member append", "x")); err == nil {
		t.Fatal("a read member could append to the shared mailbox")
	}
	for name := range folderStates(w.alice, w.aliceMailbox) {
		if strings.HasPrefix(name, "Shared") {
			t.Fatalf("alice's own folder list shows %q", name)
		}
	}
	if code := errorCode(t, w.alice, "POST", "/mailboxes/"+w.aliceMailbox+"/folders", map[string]string{"name": "Shared"}, 422); code != "invalid_folder_name" {
		t.Fatalf("creating a folder named Shared: %q, want invalid_folder_name", code)
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	full, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer full.Close()
	if err := tryAppend(full, inbox, rfc822(alice, shared.Address, "filed by alice", "x")); err != nil {
		t.Fatalf("full member append: %v", err)
	}
	if err := full.Create(inbox+"/Projects", nil).Wait(); err != nil {
		t.Fatalf("full member create subfolder: %v", err)
	}
	if err := full.SetACL(inbox, imap.RightsIdentifier(bob), imap.RightModificationReplace, imap.RightSet("lr")).Wait(); err == nil {
		t.Fatal("a member could change the shared mailbox's ACL")
	}
	names := imapNames(t, full)
	for _, special := range []string{"Sent", "Drafts", "Archive"} {
		if !slices.Contains(names, inbox+"/"+special) {
			t.Fatalf("full member does not see %s/%s: %v", inbox, special, names)
		}
	}

	own := "to alice " + randomSuffix()
	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{"to": []map[string]string{{"address": alice}}, "subject": own, "text": "hi"}, nil, 202)
	var ownUID uint32
	waitFor(t, "alice to receive mail", func() bool { ownUID = findInAPI(w.alice, w.aliceMailbox, "INBOX", own); return ownUID != 0 })
	setDelegate(t, w.admin, w.aliceMailbox, map[string]any{"email": bob, "access": "full", "sendAs": true})
	through := "/mailboxes/" + w.aliceMailbox
	for _, c := range []*apiClient{w.alice, w.bob} {
		if code := c.do("GET", through+"/messages?folder="+inbox, nil, nil); code != 404 {
			t.Fatalf("the shared mailbox is reachable through alice's mailbox as folder %s: %d", inbox, code)
		}
		if code := c.do("POST", through+"/messages/move?folder=INBOX", map[string]any{"uids": []uint32{ownUID}, "to": inbox}, nil); code != 404 {
			t.Fatalf("a message can be moved into the shared mailbox through alice's mailbox: %d", code)
		}
	}
	if code := w.alice.do("PATCH", through+"/folders?folder="+url.QueryEscape(inbox), map[string]string{"parent": "", "name": "Taken"}, nil); code != 404 {
		t.Fatalf("a shared folder can be renamed through alice's mailbox: %d", code)
	}

	stranger, err := imapLogin(t, bob, createAppPassword(t, w.bob, w.bobMailbox))
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer stranger.Close()
	for _, name := range imapNames(t, stranger) {
		if strings.HasPrefix(name, "Shared/") {
			t.Fatalf("bob is not a member but sees %s", name)
		}
	}
}

func TestSharedMailboxRevocationIsImmediate(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	member := setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	login := func() *imapclient.Client {
		t.Helper()
		ic, err := imapLogin(t, alice, w.aliceAppPassword)
		if err != nil {
			t.Fatalf("alice imap login: %v", err)
		}
		return ic
	}
	reading := func() *imapclient.Client {
		t.Helper()
		ic := login()
		if _, err := ic.Select(inbox, nil).Wait(); err != nil {
			t.Fatalf("select %s: %v", inbox, err)
		}
		if _, err := ic.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect(); err != nil {
			t.Fatalf("fetch: %v", err)
		}
		return ic
	}
	seed := login()
	if err := tryAppend(seed, inbox, rfc822("customer@external.example", shared.Address, "question", "hello")); err != nil {
		t.Fatalf("full member append: %v", err)
	}
	seed.Close()

	held := reading()
	defer held.Close()
	start := time.Now()
	w.admin.must("DELETE", "/mailboxes/"+shared.ID+"/delegates/"+member.UserID, nil, nil, 204)
	waitKicked(t, held, start)
	after := login()
	defer after.Close()
	if slices.Contains(imapNames(t, after), inbox) {
		t.Fatal("a removed member still sees the shared mailbox")
	}
	if _, err := after.Select(inbox, nil).Wait(); err == nil {
		t.Fatal("a removed member can still open the shared mailbox")
	}
	if code := w.alice.do("GET", "/mailboxes/"+shared.ID+"/messages", nil, nil); code != 404 {
		t.Fatalf("a removed member reads the shared mailbox in Aether Mail: %d", code)
	}
	var mine []sharedMailbox
	w.alice.must("GET", "/me/mailboxes", nil, &mine, 200)
	for _, m := range mine {
		if m.ID == shared.ID {
			t.Fatal("a removed member still has the shared mailbox in /me/mailboxes")
		}
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	again := reading()
	defer again.Close()
	start = time.Now()
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "read"})
	waitKicked(t, again, start)
	readOnly := login()
	defer readOnly.Close()
	if !slices.Contains(imapNames(t, readOnly), inbox) {
		t.Fatal("a member downgraded to read lost the shared mailbox")
	}
	if _, err := readOnly.Select(inbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("read member EXAMINE: %v", err)
	}
	if err := tryAppend(readOnly, inbox, rfc822(alice, shared.Address, "after downgrade", "x")); err == nil {
		t.Fatal("a member downgraded to read can still append")
	}
}

func TestSharedMailboxSendRights(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	send := "/mailboxes/" + shared.ID + "/send"
	message := func(subject string) map[string]any {
		return map[string]any{"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "from the team"}
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	if code := errorCode(t, w.alice, "POST", send, message("no send right"), 403); code != "cannot_send" {
		t.Fatalf("send without send rights: %q, want cannot_send", code)
	}
	var mine []struct {
		ID           string `json:"id"`
		SendAs       bool   `json:"sendAs"`
		SendOnBehalf bool   `json:"sendOnBehalf"`
	}
	w.alice.must("GET", "/me/mailboxes", nil, &mine, 200)
	for _, m := range mine {
		if m.ID == shared.ID && (m.SendAs || m.SendOnBehalf) {
			t.Fatalf("/me/mailboxes reports send rights that were not granted: %+v", m)
		}
	}

	bobIMAP, err := imapLogin(t, bob, createAppPassword(t, w.bob, w.bobMailbox))
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer bobIMAP.Close()
	sender := func(subject string) string { return senderHeader(t, bobIMAP, subject, shared.Address) }

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendOnBehalf": true})
	onBehalf := "on behalf " + randomSuffix()
	w.alice.must("POST", send, message(onBehalf), nil, 202)
	if got, err := mail.ParseAddress(sender(onBehalf)); err != nil || got.Address != alice {
		t.Fatalf("Sender = %v (%v), want %s", got, err, alice)
	}
	waitFor(t, "the copy in the shared Sent folder", func() bool { return findInAPI(w.alice, shared.ID, "Sent", onBehalf) != 0 })

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": true, "sendOnBehalf": false})
	sendAs := "send as " + randomSuffix()
	w.alice.must("POST", send, message(sendAs), nil, 202)
	if got := sender(sendAs); got != "" {
		t.Fatalf("send as added Sender %q", got)
	}
	draft := "draft kept " + randomSuffix()
	var saved struct {
		UID uint32 `json:"uid"`
	}
	w.alice.must("PUT", "/mailboxes/"+shared.ID+"/drafts", map[string]any{"subject": draft, "text": "unsent"}, &saved, 200)

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "read", "sendAs": true})
	readSend := message("read and send as " + randomSuffix())
	readSend["draftUid"] = saved.UID
	w.alice.must("POST", send, readSend, nil, 202)
	if findInAPI(w.alice, shared.ID, "Drafts", draft) == 0 {
		t.Fatal("a read member's send removed a draft from the shared mailbox")
	}
	var uid uint32
	waitFor(t, "the send as copy in the shared Sent folder", func() bool { uid = findInAPI(w.alice, shared.ID, "Sent", sendAs); return uid != 0 })
	if code := w.alice.do("PATCH", "/mailboxes/"+shared.ID+"/messages/"+itoaUID(uid)+"?folder=Sent", map[string]bool{"seen": false}, nil); code != 403 {
		t.Fatalf("a read member with send as changed flags: %d", code)
	}
	if code := w.alice.do("PUT", "/mailboxes/"+shared.ID+"/drafts", message("draft"), nil); code != 403 {
		t.Fatalf("a read member with send as saved a draft: %d", code)
	}
}

func TestSharedMailboxSendAsOverSMTP(t *testing.T) {
	w := setupWorld(t)
	submission := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	support := shared.Address

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": true})
	subject := "smtp send as " + randomSuffix()
	if err := submit(t, submission, alice, w.aliceAppPassword, support, []string{bob}, rfc822(support, bob, subject, "from a mail app"), false); err != nil {
		t.Fatalf("send as over SMTP: %v", err)
	}
	waitFor(t, "bob to receive the send as message", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": false, "sendOnBehalf": true})
	if err := submit(t, submission, alice, w.aliceAppPassword, support, []string{bob}, rfc822(support, bob, "on behalf envelope", "x"), false); err == nil {
		t.Fatal("send on behalf alone allowed the shared address as envelope sender over SMTP")
	}
	if err := submit(t, submission, alice, w.aliceAppPassword, alice, []string{bob}, "Sender: "+alice+"\r\n"+rfc822(support, bob, "on behalf header", "x"), false); err == nil {
		t.Fatal("SMTP accepted From: the shared address with the member's envelope")
	}
	if err := submit(t, submission, bob, createAppPassword(t, w.bob, w.bobMailbox), support, []string{bob}, rfc822(support, bob, "not a member", "x"), false); err == nil {
		t.Fatal("a non-member used the shared address as envelope sender")
	}
}

func TestSharedMailboxScheduledSendChecksRights(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	member := setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": true})
	subject := "scheduled from support " + randomSuffix()
	w.alice.must("POST", "/mailboxes/"+shared.ID+"/send", map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "sent later",
		"sendAt": time.Now().Add(6 * time.Second).UTC().Format(time.RFC3339),
	}, nil, 202)
	w.admin.must("DELETE", "/mailboxes/"+shared.ID+"/delegates/"+member.UserID, nil, nil, 204)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": bob, "access": "read"})

	var list []struct {
		Subject string  `json:"subject"`
		Error   *string `json:"error"`
	}
	waitFor(t, "the scheduled message to fail", func() bool {
		w.bob.must("GET", "/mailboxes/"+shared.ID+"/scheduled", nil, &list, 200)
		return len(list) == 1 && list[0].Error != nil
	})
	if list[0].Subject != subject || !strings.Contains(*list[0].Error, "no longer have permission") {
		t.Fatalf("scheduled item = %q with error %q", list[0].Subject, *list[0].Error)
	}
	if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 {
		t.Fatal("a message scheduled by a removed member was sent")
	}
}

func TestMailboxConversion(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliceID, bobID := userID(t, w.alice), userID(t, w.bob)
	convert := "/mailboxes/" + w.aliceMailbox + "/convert"
	delegates := "/mailboxes/" + w.aliceMailbox + "/delegates"

	var converted sharedMailbox
	w.admin.must("POST", convert, map[string]string{"kind": "shared"}, &converted, 200)
	if converted.Kind != "shared" || converted.OwnerUserID != nil {
		t.Fatalf("converted %+v, want an ownerless shared mailbox", converted)
	}
	if code := w.alice.do("GET", "/session", nil, nil); code != 401 {
		t.Fatalf("the former owner's session answered %d, want 401", code)
	}
	if ic, err := imapLogin(t, alice, w.aliceAppPassword); err == nil {
		ic.Close()
		t.Fatal("the former owner's app password still works over IMAP")
	}
	if code := errorCode(t, newClient(t), "POST", "/auth/login", map[string]string{"email": alice, "password": "alice-integration-pw"}, 403); code != "account_disabled" {
		t.Fatalf("former owner sign-in: %q, want account_disabled", code)
	}
	var members []delegateView
	w.admin.must("GET", delegates, nil, &members, 200)
	if len(members) != 0 {
		t.Fatalf("a converted mailbox starts with members %+v", members)
	}
	setDelegate(t, w.admin, w.aliceMailbox, map[string]any{"email": bob, "access": "full", "sendAs": true})
	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	bobIMAP, err := imapLogin(t, bob, bobPass)
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer bobIMAP.Close()
	if !slices.Contains(imapNames(t, bobIMAP), "Shared/"+alice) {
		t.Fatal("bob does not see the converted mailbox")
	}

	w.admin.must("POST", convert, map[string]string{"kind": "user"}, &converted, 200)
	if converted.Kind != "user" || converted.OwnerUserID == nil || *converted.OwnerUserID != aliceID {
		t.Fatalf("converted back %+v, want alice as the owner", converted)
	}
	w.alice.login(alice, "alice-integration-pw")
	again, err := imapLogin(t, bob, bobPass)
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer again.Close()
	if slices.Contains(imapNames(t, again), "Shared/"+alice) {
		t.Fatal("bob still sees a mailbox that is no longer shared")
	}
	w.admin.must("GET", delegates, nil, &members, 200)
	if len(members) != 1 || members[0].UserID != bobID || !members[0].SendAs {
		t.Fatalf("delegates after converting back = %+v, want bob with send as", members)
	}

	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider idOnly
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret, "autoProvision": true, "adminGroup": "aether-admins",
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)
	f.subject, f.email, f.groups = "halo-"+randomSuffix(), "root@"+w.domain, []string{"aether-admins"}
	root := oidcLogin(t, f, slug)
	defer w.admin.do("PATCH", "/admin/users/"+userID(t, root), map[string]string{"status": "suspended"}, nil)
	var boxes []sharedMailbox
	root.must("GET", "/me/mailboxes", nil, &boxes, 200)
	if len(boxes) != 1 {
		t.Fatalf("platform administrator mailboxes = %+v", boxes)
	}
	if code := errorCode(t, w.admin, "POST", "/mailboxes/"+boxes[0].ID+"/convert", map[string]string{"kind": "shared"}, 422); code != "owner_is_admin" {
		t.Fatalf("converting a platform administrator's mailbox: %q, want owner_is_admin", code)
	}
}

func TestSharedMailboxIsNeverAutoLinked(t *testing.T) {
	w := setupWorld(t)
	shared := createSharedMailbox(t, w)
	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider idOnly
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret, "autoProvision": true,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)

	f.subject, f.email = "halo-"+randomSuffix(), shared.Address
	browser, callback, _ := oidcAuthorize(t, f, slug)
	resp, err := browser.http.Get(callback)
	if err != nil || !strings.Contains(resp.Header.Get("Location"), "error=shared_mailbox") {
		t.Fatalf("OIDC sign-in with the shared address: %v %v", err, resp)
	}
	var boxes []sharedMailbox
	w.admin.must("GET", "/organizations/"+w.orgID+"/mailboxes", nil, &boxes, 200)
	for _, m := range boxes {
		if m.ID == shared.ID && (m.Kind != "shared" || m.OwnerUserID != nil) {
			t.Fatalf("the shared mailbox was linked to an account: %+v", m)
		}
	}
	var orgMembers []struct {
		Email string `json:"email"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/members", nil, &orgMembers, 200)
	for _, m := range orgMembers {
		if m.Email == shared.Address {
			t.Fatal("an account with the shared address joined the organization")
		}
	}

	var scim struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/admin/identity-providers/"+provider.ID+"/scim-token", nil, &scim, 200)
	halo := newClient(t)
	halo.bearer = scim.Token
	body := map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": shared.Address, "externalId": "halo-" + randomSuffix(), "active": true}
	if got := halo.do("POST", "/scim/"+slug+"/Users", body, nil); got != 400 {
		t.Fatalf("SCIM create with the shared address returned %d, want 400", got)
	}
	var list struct {
		Resources []scimUser `json:"Resources"`
	}
	halo.must("GET", "/scim/"+slug+"/Users?filter="+url.QueryEscape(`userName eq "`+shared.Address+`"`), nil, &list, 200)
	if len(list.Resources) != 0 {
		t.Fatalf("SCIM created a user for the shared address: %+v", list.Resources)
	}
}

func TestSharedMailboxAPIKeyManagement(t *testing.T) {
	w := setupWorld(t)
	shared := createSharedMailbox(t, w)
	keyFor := func(scope string) *apiClient {
		var created struct {
			Token string `json:"token"`
		}
		w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{"name": "shared " + scope, "scopes": []string{scope}}, &created, 201)
		return &apiClient{t: t, base: w.admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: created.Token}
	}
	reader, writer := keyFor("mailboxes:read"), keyFor("mailboxes:write")
	member := map[string]any{"email": "alice@" + w.domain, "sendAs": true}
	convert := map[string]string{"kind": "user"}

	if code := reader.do("POST", "/mailboxes/"+shared.ID+"/delegates", member, nil); code != 403 {
		t.Fatalf("a read key added a member: %d", code)
	}
	if code := reader.do("POST", "/mailboxes/"+shared.ID+"/convert", convert, nil); code != 403 {
		t.Fatalf("a read key converted a mailbox: %d", code)
	}
	if d := setDelegate(t, writer, shared.ID, member); !d.SendAs {
		t.Fatalf("member added with an API key = %+v, want send as", d)
	}
	writer.must("POST", "/mailboxes/"+shared.ID+"/convert", convert, nil, 200)

	var events []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &events, 200)
	var actions []string
	for _, e := range events {
		actions = append(actions, e.Action)
	}
	if !slices.Contains(actions, "mailbox.delegate_add") || !slices.Contains(actions, "mailbox.convert") {
		t.Fatalf("audit log %v is missing the API key's membership or conversion change", actions)
	}
}

func TestSharedMailboxACLSyncOutrunsFolderCreation(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	member := setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	login := func() *imapclient.Client {
		t.Helper()
		ic, err := imapLogin(t, alice, w.aliceAppPassword)
		if err != nil {
			t.Fatalf("alice imap login: %v", err)
		}
		return ic
	}
	seed := login()
	for i := range 30 {
		if err := seed.Create(fmt.Sprintf("%s/Bulk-%02d", inbox, i), nil).Wait(); err != nil {
			t.Fatalf("create folder: %v", err)
		}
	}
	seed.Close()
	racing := func(change func()) []string {
		t.Helper()
		creator := login()
		defer creator.Close()
		prefix := inbox + "/Racing-" + randomSuffix() + "-"
		var created []string
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; ; i++ {
				name := fmt.Sprintf("%s%03d", prefix, i)
				if err := creator.Create(name, nil).Wait(); err != nil {
					return
				}
				created = append(created, name)
				if err := creator.Create(name+"/Sub", nil).Wait(); err != nil {
					return
				}
				created = append(created, name+"/Sub")
			}
		}()
		change()
		<-done
		if len(created) == 0 {
			t.Fatal("no folder was created during the change; the race was not exercised")
		}
		return created
	}

	created := racing(func() { w.admin.must("DELETE", "/mailboxes/"+shared.ID+"/delegates/"+member.UserID, nil, nil, 204) })
	after := login()
	defer after.Close()
	for _, name := range imapNames(t, after) {
		if strings.HasPrefix(name, "Shared/") {
			t.Fatalf("a removed member still lists %s", name)
		}
	}
	for _, name := range created {
		if _, err := after.Select(name, nil).Wait(); err == nil {
			t.Fatalf("a removed member can still open %s, created during the removal", name)
		}
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	created = racing(func() { setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "read"}) })
	readOnly := login()
	defer readOnly.Close()
	var listed []string
	for _, name := range imapNames(t, readOnly) {
		if strings.HasPrefix(name, "Shared/") {
			listed = append(listed, name)
		}
	}
	for _, name := range append(listed, created...) {
		if err := tryAppend(readOnly, name, rfc822(alice, shared.Address, "after downgrade", "x")); err == nil {
			t.Fatalf("a member downgraded to read can still append to %s", name)
		}
	}
}

func TestSharedMailboxRSVPAndAutoReplyNeedSendRights(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	title := "Sync " + randomSuffix()
	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour).UTC()
	w.bob.must("POST", "/calendar/events", map[string]any{"title": title, "start": start, "end": start.Add(time.Hour), "attendees": []string{shared.Address}}, nil, 201)
	var inviteUID uint32
	waitFor(t, "the invitation in the shared inbox", func() bool {
		inviteUID = findInAPI(w.alice, shared.ID, "INBOX", "Invitation: "+title)
		return inviteUID != 0
	})
	rsvp := "/mailboxes/" + shared.ID + "/messages/" + itoaUID(inviteUID) + "/invitation"
	autoReply := "/mailboxes/" + shared.ID + "/auto-reply"
	away := map[string]any{"enabled": true, "subject": "Away " + randomSuffix(), "message": "Back soon."}
	if code := errorCode(t, w.alice, "POST", rsvp, map[string]string{"response": "accepted"}, 403); code != "cannot_send" {
		t.Fatalf("RSVP without a send right: %q, want cannot_send", code)
	}
	if code := errorCode(t, w.alice, "PUT", autoReply, away, 403); code != "cannot_send" {
		t.Fatalf("auto-reply without a send right: %q, want cannot_send", code)
	}
	if code := errorCode(t, w.alice, "GET", autoReply, nil, 403); code != "cannot_send" {
		t.Fatalf("reading the auto-reply without a send right: %q, want cannot_send", code)
	}
	bobIMAP, err := imapLogin(t, bob, createAppPassword(t, w.bob, w.bobMailbox))
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer bobIMAP.Close()

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendOnBehalf": true})
	w.alice.must("POST", rsvp, map[string]string{"response": "accepted"}, nil, 200)
	if got, err := mail.ParseAddress(senderHeader(t, bobIMAP, "Accepted: "+title, shared.Address)); err != nil || got.Address != alice {
		t.Fatalf("Sender = %v (%v), want %s", got, err, alice)
	}
	if code := w.alice.do("PUT", autoReply, away, nil); code != 403 {
		t.Fatalf("send on behalf alone enabled an auto-reply: %d", code)
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": true, "sendOnBehalf": false})
	w.alice.must("POST", rsvp, map[string]string{"response": "declined"}, nil, 200)
	if got := senderHeader(t, bobIMAP, "Declined: "+title, shared.Address); got != "" {
		t.Fatalf("send as added Sender %q", got)
	}
	w.alice.must("PUT", autoReply, away, nil, 200)
	w.alice.must("GET", autoReply, nil, nil, 200)
	w.admin.must("PUT", autoReply, map[string]any{"enabled": false, "subject": "", "message": ""}, nil, 200)
}

func TestConvertedAccountCannotBeReactivated(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	aliceID := userID(t, w.alice)
	f := startFakeIssuer(t)
	slug, secret := "halo-"+randomSuffix(), "whsec-"+randomSuffix()
	var provider idOnly
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret, "autoProvision": true, "webhookSecret": secret,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)
	f.subject, f.email = "halo-"+randomSuffix(), alice
	oidcLogin(t, f, slug)
	var scim struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/admin/identity-providers/"+provider.ID+"/scim-token", nil, &scim, 200)
	halo := newClient(t)
	halo.bearer = scim.Token
	hook := env(t, "AETHER_TEST_API", "http://api:8080") + "/api/v1/hooks/identity-providers/" + slug
	convert := "/mailboxes/" + w.aliceMailbox + "/convert"
	w.admin.must("POST", convert, map[string]string{"kind": "shared"}, nil, 200)

	blocked := func(step string) {
		t.Helper()
		if code := errorCode(t, newClient(t), "POST", "/auth/login", map[string]string{"email": alice, "password": "alice-integration-pw"}, 403); code != "account_disabled" {
			t.Fatalf("%s: password sign-in answered %q, want account_disabled", step, code)
		}
		browser, callback, _ := oidcAuthorize(t, f, slug)
		resp, err := browser.http.Get(callback)
		if err != nil || !strings.Contains(resp.Header.Get("Location"), "error=account_disabled") {
			t.Fatalf("%s: OIDC sign-in answered %v %v, want account_disabled", step, err, resp)
		}
	}
	if code := errorCode(t, w.admin, "PATCH", "/admin/users/"+aliceID, map[string]string{"status": "active"}, 409); code != "mailbox_converted" {
		t.Fatalf("administrator restore of a converted account: %q, want mailbox_converted", code)
	}
	blocked("administrator restore")
	patch := func(active bool) map[string]any {
		return map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []map[string]any{{"op": "replace", "path": "active", "value": active}}}
	}
	halo.must("PATCH", "/scim/"+slug+"/Users/"+aliceID, patch(false), nil, 200)
	halo.must("PATCH", "/scim/"+slug+"/Users/"+aliceID, patch(true), nil, 200)
	blocked("SCIM deactivate then activate")
	for _, event := range []string{"user.suspend", "user.restore"} {
		if got := signedEvent(t, hook, secret, event, f.subject); got != 204 {
			t.Fatalf("%s webhook returned %d", event, got)
		}
	}
	blocked("provider suspend then restore")

	w.admin.must("POST", convert, map[string]string{"kind": "user"}, nil, 200)
	w.alice.login(alice, "alice-integration-pw")
}

func TestOrganizationRemovalRevokesMailAccess(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliceID := userID(t, w.alice)
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	setDelegate(t, w.admin, w.bobMailbox, map[string]any{"email": alice, "access": "full", "sendAs": true})
	delegated := createAppPassword(t, w.alice, w.bobMailbox)
	held, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer held.Close()
	if err := tryAppend(held, inbox, rfc822("customer@external.example", shared.Address, "question", "hello")); err != nil {
		t.Fatalf("full member append: %v", err)
	}
	if _, err := held.Select(inbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", inbox, err)
	}
	asBob, err := imapLogin(t, bob, delegated)
	if err != nil {
		t.Fatalf("delegated app password login: %v", err)
	}
	defer asBob.Close()
	if err := tryAppend(asBob, "INBOX", rfc822(alice, bob, "for bob", "x")); err != nil {
		t.Fatalf("delegate append: %v", err)
	}
	if _, err := asBob.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select INBOX as bob: %v", err)
	}

	start := time.Now()
	w.admin.must("DELETE", "/organizations/"+w.orgID+"/members/"+aliceID, nil, nil, 204)
	waitKicked(t, held, start)
	waitKicked(t, asBob, start)
	after, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice's own login after removal: %v", err)
	}
	defer after.Close()
	if slices.Contains(imapNames(t, after), inbox) {
		t.Fatal("a removed organization member still lists the shared mailbox")
	}
	if _, err := after.Select(inbox, nil).Wait(); err == nil {
		t.Fatal("a removed organization member can still open the shared mailbox")
	}
	if ic, err := imapLogin(t, bob, delegated); err == nil {
		ic.Close()
		t.Fatal("the delegated app password survived organization removal")
	}

	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": alice, "role": "member"}, nil, 204)
	setDelegate(t, w.admin, w.bobMailbox, map[string]any{"email": alice, "access": "full", "sendAs": true})
	if ic, err := imapLogin(t, bob, delegated); err == nil {
		ic.Close()
		t.Fatal("the delegated app password came back when the member was re-added")
	}

	type event struct {
		Action   string `json:"action"`
		TargetID string `json:"targetId"`
	}
	var events []event
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &events, 200)
	for _, id := range []string{shared.ID, w.bobMailbox} {
		if !slices.ContainsFunc(events, func(e event) bool { return e.Action == "mailbox.delegate_remove" && e.TargetID == id }) {
			t.Fatalf("no mailbox.delegate_remove audit event for %s after organization removal", id)
		}
	}
}

func TestSuspensionEndsSharedAndDelegatedSessions(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliceID := userID(t, w.alice)
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	setDelegate(t, w.admin, w.bobMailbox, map[string]any{"email": alice, "access": "full", "sendAs": true})
	delegated := createAppPassword(t, w.alice, w.bobMailbox)
	held, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer held.Close()
	if err := tryAppend(held, inbox, rfc822("customer@external.example", shared.Address, "question", "hello")); err != nil {
		t.Fatalf("full member append: %v", err)
	}
	if _, err := held.Select(inbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", inbox, err)
	}
	asBob, err := imapLogin(t, bob, delegated)
	if err != nil {
		t.Fatalf("delegated app password login: %v", err)
	}
	defer asBob.Close()
	if err := tryAppend(asBob, "INBOX", rfc822(alice, bob, "for bob", "x")); err != nil {
		t.Fatalf("delegate append: %v", err)
	}
	if _, err := asBob.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select INBOX as bob: %v", err)
	}

	start := time.Now()
	w.admin.must("PATCH", "/admin/users/"+aliceID, map[string]string{"status": "suspended"}, nil, 204)
	waitKicked(t, held, start)
	waitKicked(t, asBob, start)
	if ic, err := imapLogin(t, bob, delegated); err == nil {
		ic.Close()
		t.Fatal("a suspended user's delegated app password still signs in")
	}

	w.admin.must("PATCH", "/admin/users/"+aliceID, map[string]string{"status": "active"}, nil, 204)
	back, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login after restore: %v", err)
	}
	defer back.Close()
	if !slices.Contains(imapNames(t, back), inbox) {
		t.Fatal("a restored member lost the shared mailbox")
	}
	if err := tryAppend(back, inbox, rfc822(alice, shared.Address, "after restore", "x")); err != nil {
		t.Fatalf("restored full member append: %v", err)
	}
	again, err := imapLogin(t, bob, delegated)
	if err != nil {
		t.Fatalf("the delegated app password did not come back after restore: %v", err)
	}
	again.Close()
}

func TestSharedMailboxScheduledSendModes(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliceID := userID(t, w.alice)
	shared := createSharedMailbox(t, w)
	send := "/mailboxes/" + shared.ID + "/send"
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": bob, "access": "read"})
	bobIMAP, err := imapLogin(t, bob, createAppPassword(t, w.bob, w.bobMailbox))
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer bobIMAP.Close()
	schedule := func(subject string) {
		t.Helper()
		w.alice.must("POST", send, map[string]any{
			"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "later",
			"sendAt": time.Now().Add(6 * time.Second).UTC().Format(time.RFC3339),
		}, nil, 202)
	}
	type item struct {
		Subject string  `json:"subject"`
		Error   *string `json:"error"`
	}
	failed := func(subject string) {
		t.Helper()
		var list []item
		waitFor(t, subject+" to fail", func() bool {
			w.bob.must("GET", "/mailboxes/"+shared.ID+"/scheduled", nil, &list, 200)
			return slices.ContainsFunc(list, func(s item) bool { return s.Subject == subject && s.Error != nil })
		})
		for _, s := range list {
			if s.Subject == subject && !strings.Contains(*s.Error, "no longer have permission") {
				t.Fatalf("%s failed with %q", subject, *s.Error)
			}
		}
		if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 {
			t.Fatalf("%s was delivered", subject)
		}
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": true, "sendOnBehalf": true})
	sendAsLost := "send as then on behalf only " + randomSuffix()
	schedule(sendAsLost)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": false})
	onBehalfKept := "on behalf kept " + randomSuffix()
	schedule(onBehalfKept)
	failed(sendAsLost)
	if got, err := mail.ParseAddress(senderHeader(t, bobIMAP, onBehalfKept, shared.Address)); err != nil || got.Address != alice {
		t.Fatalf("Sender = %v (%v), want %s", got, err, alice)
	}

	onBehalfLost := "on behalf then send as only " + randomSuffix()
	schedule(onBehalfLost)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": true, "sendOnBehalf": false})
	sendAsKept := "send as kept " + randomSuffix()
	schedule(sendAsKept)
	failed(onBehalfLost)
	if got := senderHeader(t, bobIMAP, sendAsKept, shared.Address); got != "" {
		t.Fatalf("send as added Sender %q", got)
	}

	suspended := "queued then suspended " + randomSuffix()
	schedule(suspended)
	w.admin.must("PATCH", "/admin/users/"+aliceID, map[string]string{"status": "suspended"}, nil, 204)
	failed(suspended)
}

func TestScheduledSendNeverWritesForReadMembers(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	send := "/mailboxes/" + shared.ID + "/send"
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full", "sendAs": true})
	original := "ticket " + randomSuffix()
	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{"to": []map[string]string{{"address": shared.Address}}, "subject": original, "text": "help"}, nil, 202)
	var uid uint32
	waitFor(t, "the ticket in the shared inbox", func() bool { uid = findInAPI(w.alice, shared.ID, "INBOX", original); return uid != 0 })
	var ticket struct {
		MessageID string `json:"messageId"`
	}
	w.alice.must("GET", "/mailboxes/"+shared.ID+"/messages/"+itoaUID(uid)+"?folder=INBOX", nil, &ticket, 200)
	draft := "draft kept " + randomSuffix()
	var saved struct {
		UID uint32 `json:"uid"`
	}
	w.alice.must("PUT", "/mailboxes/"+shared.ID+"/drafts", map[string]any{"subject": draft, "text": "unsent"}, &saved, 200)
	answered := func() bool {
		var page messagePage
		w.alice.must("GET", "/mailboxes/"+shared.ID+"/messages?folder=INBOX", nil, &page, 200)
		for _, m := range page.Messages {
			if m.Subject == original {
				return m.Answered
			}
		}
		t.Fatalf("%s disappeared from the shared inbox", original)
		return false
	}
	reply := func(subject string, draftUID uint32) {
		t.Helper()
		w.alice.must("POST", send, map[string]any{
			"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "re", "inReplyTo": ticket.MessageID, "draftUid": draftUID,
			"sendAt": time.Now().Add(6 * time.Second).UTC().Format(time.RFC3339),
		}, nil, 202)
		waitFor(t, "bob to receive "+subject, func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })
		waitFor(t, "the copy of "+subject+" in the shared Sent folder", func() bool { return findInAPI(w.alice, shared.ID, "Sent", subject) != 0 })
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "read"})
	reply("scheduled by a reader "+randomSuffix(), saved.UID)
	if findInAPI(w.alice, shared.ID, "Drafts", draft) == 0 {
		t.Fatal("scheduling by a read member removed a draft from the shared mailbox")
	}
	if answered() {
		t.Fatal("a read member's scheduled reply marked the shared message answered")
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	downgraded := "scheduled then downgraded " + randomSuffix()
	w.alice.must("POST", send, map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": downgraded, "text": "re", "inReplyTo": ticket.MessageID,
		"sendAt": time.Now().Add(6 * time.Second).UTC().Format(time.RFC3339),
	}, nil, 202)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "read"})
	waitFor(t, "bob to receive "+downgraded, func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", downgraded) != 0 })
	waitFor(t, "the copy of "+downgraded+" in the shared Sent folder", func() bool { return findInAPI(w.alice, shared.ID, "Sent", downgraded) != 0 })
	if answered() {
		t.Fatal("a scheduled reply marked the shared message answered after its sender was downgraded")
	}

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	reply("scheduled by a full member "+randomSuffix(), 0)
	waitFor(t, "the ticket to be marked answered by a full member's reply", answered)
}

func TestScheduledMessageWithoutSenderFailsClosed(t *testing.T) {
	db := testDB(t)
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "sendAs": true})
	subject := "without a sender " + randomSuffix()
	var item idOnly
	w.alice.must("POST", "/mailboxes/"+shared.ID+"/send", map[string]any{
		"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "later",
		"sendAt": time.Now().Add(6 * time.Second).UTC().Format(time.RFC3339),
	}, &item, 202)
	tag, err := db.Exec(context.Background(), `UPDATE scheduled_messages SET sender_user_id = NULL WHERE id = $1`, item.ID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("clearing the sender: %v (%d rows)", err, tag.RowsAffected())
	}
	var list []struct {
		Error *string `json:"error"`
	}
	waitFor(t, "the message without a sender to fail", func() bool {
		w.alice.must("GET", "/mailboxes/"+shared.ID+"/scheduled", nil, &list, 200)
		return len(list) == 1 && list[0].Error != nil
	})
	if !strings.Contains(*list[0].Error, "no longer have permission") {
		t.Fatalf("error = %q", *list[0].Error)
	}
	if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 {
		t.Fatal("a scheduled message without a sender was delivered")
	}
}

func TestConversionRevokesEveryMailIdentity(t *testing.T) {
	db := testDB(t)
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	convert := "/mailboxes/" + w.aliceMailbox + "/convert"
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	setDelegate(t, w.admin, w.bobMailbox, map[string]any{"email": alice, "access": "full", "sendAs": true})
	f, _, _ := linkedIssuer(t, w, alice)
	token := f.mint(f.subject, f.clientID)
	delegated := createAppPassword(t, w.alice, w.bobMailbox)
	member, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer member.Close()
	if err := tryAppend(member, inbox, rfc822("customer@external.example", shared.Address, "question", "hello")); err != nil {
		t.Fatalf("full member append: %v", err)
	}
	if _, err := member.Select(inbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", inbox, err)
	}
	byPassword, err := imapLogin(t, bob, delegated)
	if err != nil {
		t.Fatalf("delegated app password login: %v", err)
	}
	defer byPassword.Close()
	if err := tryAppend(byPassword, "INBOX", rfc822(alice, bob, "for bob", "x")); err != nil {
		t.Fatalf("delegate append: %v", err)
	}
	if _, err := byPassword.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select INBOX as bob: %v", err)
	}
	byToken, err := imapTokenLogin(t, bob, token)
	if err != nil {
		t.Fatalf("token login as bob: %v", err)
	}
	defer byToken.Close()
	if _, err := byToken.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select INBOX as bob with a token: %v", err)
	}

	start := time.Now()
	w.admin.must("POST", convert, map[string]string{"kind": "shared"}, nil, 200)
	for _, held := range []*imapclient.Client{member, byPassword, byToken} {
		waitKicked(t, held, start)
	}
	for name, login := range map[string]func() (*imapclient.Client, error){
		"alice's own app password":   func() (*imapclient.Client, error) { return imapLogin(t, alice, w.aliceAppPassword) },
		"the delegated app password": func() (*imapclient.Client, error) { return imapLogin(t, bob, delegated) },
		"a token login as bob":       func() (*imapclient.Client, error) { return imapTokenLogin(t, bob, token) },
	} {
		if ic, err := login(); err == nil {
			ic.Close()
			t.Fatalf("%s still signs in after the conversion", name)
		}
	}
	var pending bool
	if err := db.QueryRow(context.Background(), `SELECT acl_pending FROM mailboxes WHERE id = $1`, shared.ID).Scan(&pending); err != nil || pending {
		t.Fatalf("the former owner's shared membership was not reconciled after the conversion (pending=%v, err=%v)", pending, err)
	}
	own, err := imapLogin(t, bob, createAppPassword(t, w.bob, w.bobMailbox))
	if err != nil {
		t.Fatalf("bob's own login after the conversion: %v", err)
	}
	own.Close()

	w.admin.must("POST", convert, map[string]string{"kind": "user"}, nil, 200)
	w.alice.login(alice, "alice-integration-pw")
	if ic, err := imapLogin(t, bob, delegated); err == nil {
		ic.Close()
		t.Fatal("the revoked delegated app password came back with the account")
	}
	restored, err := imapLogin(t, alice, createAppPassword(t, w.alice, w.aliceMailbox))
	if err != nil {
		t.Fatalf("alice imap login after converting back: %v", err)
	}
	defer restored.Close()
	if !slices.Contains(imapNames(t, restored), inbox) {
		t.Fatal("the restored owner lost the shared mailbox")
	}
	if err := tryAppend(restored, inbox, rfc822(alice, shared.Address, "after restore", "x")); err != nil {
		t.Fatalf("restored full member append: %v", err)
	}
	again, err := imapTokenLogin(t, bob, token)
	if err != nil {
		t.Fatalf("the still-delegated token login did not come back with the account: %v", err)
	}
	again.Close()
}

func TestSharedMailboxDisableAndDeleteEndSessions(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	path := "/mailboxes/" + shared.ID
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": bob, "access": "read"})
	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	hold := func() (*imapclient.Client, *imapclient.Client) {
		t.Helper()
		full, err := imapLogin(t, alice, w.aliceAppPassword)
		if err != nil {
			t.Fatalf("alice imap login: %v", err)
		}
		if err := tryAppend(full, inbox, rfc822("customer@external.example", shared.Address, "question", "hello")); err != nil {
			t.Fatalf("full member append: %v", err)
		}
		if _, err := full.Select(inbox, nil).Wait(); err != nil {
			t.Fatalf("select %s: %v", inbox, err)
		}
		read, err := imapLogin(t, bob, bobPass)
		if err != nil {
			t.Fatalf("bob imap login: %v", err)
		}
		if _, err := read.Select(inbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			t.Fatalf("read member EXAMINE %s: %v", inbox, err)
		}
		return full, read
	}
	gone := func(step string) {
		t.Helper()
		fresh, err := imapLogin(t, alice, w.aliceAppPassword)
		if err != nil {
			t.Fatalf("%s: alice imap login: %v", step, err)
		}
		defer fresh.Close()
		if _, err := fresh.Select(inbox, nil).Wait(); err == nil {
			t.Fatalf("%s: a member can still open %s", step, inbox)
		}
		if code := w.alice.do("GET", path+"/messages", nil, nil); code != 404 {
			t.Fatalf("%s: the shared mailbox still answers in Aether Mail: %d", step, code)
		}
	}

	full, read := hold()
	defer full.Close()
	defer read.Close()
	start := time.Now()
	w.admin.must("PATCH", path, map[string]any{"active": false}, nil, 200)
	waitKicked(t, full, start)
	waitKicked(t, read, start)
	gone("disable")

	w.admin.must("PATCH", path, map[string]any{"active": true}, nil, 200)
	full, read = hold()
	defer full.Close()
	defer read.Close()
	start = time.Now()
	w.admin.must("DELETE", path, nil, nil, 204)
	waitKicked(t, full, start)
	waitKicked(t, read, start)
	fresh, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login after the deletion: %v", err)
	}
	defer fresh.Close()
	if slices.Contains(imapNames(t, fresh), inbox) {
		t.Fatal("a deleted shared mailbox is still listed")
	}

	own, err := imapLogin(t, bob, bobPass)
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer own.Close()
	if err := tryAppend(own, "INBOX", rfc822(alice, bob, "for bob", "x")); err != nil {
		t.Fatalf("bob append: %v", err)
	}
	if _, err := own.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select INBOX as bob: %v", err)
	}
	start = time.Now()
	w.admin.must("PATCH", "/mailboxes/"+w.bobMailbox, map[string]any{"active": false}, nil, 200)
	waitKicked(t, own, start)
}

func TestSharedMailboxReactivationExposesNothing(t *testing.T) {
	db := testDB(t)
	imapProxy := dovecotProxy(t, "imap")
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	bobID := userID(t, w.bob)
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	secret := inbox + "/Zz-secret"
	path := "/mailboxes/" + shared.ID
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": bob, "access": "full"})
	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	imapAddr, tlsConf := env(t, "AETHER_TEST_IMAP", "dovecot:143"), mailTLS(t)
	ctx := context.Background()
	seed, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	for i := range 300 {
		if err := seed.Create(fmt.Sprintf("%s/Bulk-%03d", inbox, i), nil).Wait(); err != nil {
			t.Fatalf("create folder: %v", err)
		}
	}
	if err := seed.Create(secret, nil).Wait(); err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if err := tryAppend(seed, secret, rfc822("customer@external.example", shared.Address, "confidential", "hello")); err != nil {
		t.Fatalf("seed the secret: %v", err)
	}
	seed.Close()
	state := func(step string, active, pending bool) {
		t.Helper()
		var gotActive, gotPending bool
		if err := db.QueryRow(ctx, `SELECT active, acl_pending FROM mailboxes WHERE id = $1`, shared.ID).Scan(&gotActive, &gotPending); err != nil {
			t.Fatalf("%s: read the mailbox row: %v", step, err)
		}
		if gotActive != active || gotPending != pending {
			t.Fatalf("%s: active=%v pending=%v, want active=%v pending=%v", step, gotActive, gotPending, active, pending)
		}
	}
	type sight struct{ failed, listed, opened, read bool }
	bobSees := func() sight {
		b, err := imapclient.DialStartTLS(imapAddr, &imapclient.Options{TLSConfig: tlsConf})
		if err != nil {
			return sight{failed: true}
		}
		defer b.Close()
		if err := b.Login(bob, bobPass).Wait(); err != nil {
			return sight{failed: true}
		}
		var s sight
		list, err := b.List("", "Shared/*", nil).Collect()
		s.listed = err == nil && len(list) > 0
		if _, err := b.Select(secret, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return s
		}
		s.opened = true
		msgs, err := b.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Envelope: true}).Collect()
		s.read = err == nil && len(msgs) == 1
		return s
	}

	imapProxy.cut()
	if code := errorCode(t, w.admin, "PATCH", path, map[string]any{"active": false}, 503); code != "mail_unavailable" {
		t.Fatalf("deactivation with Dovecot unreachable: %q, want mail_unavailable", code)
	}
	imapProxy.restore()
	state("after a refused deactivation", true, false)
	if s := bobSees(); !s.opened || !s.read {
		t.Fatalf("a refused deactivation changed the members' access: %+v", s)
	}

	w.admin.must("PATCH", path, map[string]any{"active": false}, nil, 200)
	state("after disabling", false, false)
	w.admin.must("DELETE", path+"/delegates/"+bobID, nil, nil, 204)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "read"})
	state("after changing members while inactive", false, true)
	if _, err := db.Exec(ctx, `UPDATE mailboxes SET active = true, acl_pending = false WHERE id = $1`, shared.ID); err != nil {
		t.Fatalf("raw reactivation: %v", err)
	}
	if s := bobSees(); s.failed || s.listed || s.opened {
		t.Fatalf("an inactive shared mailbox still carried a member's rights in Dovecot: %+v", s)
	}
	if _, err := db.Exec(ctx, `UPDATE mailboxes SET active = false, acl_pending = true WHERE id = $1`, shared.ID); err != nil {
		t.Fatalf("undo the raw reactivation: %v", err)
	}

	var stop atomic.Bool
	var attempts, failed, listed, opened, read atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				s := bobSees()
				attempts.Add(1)
				for _, c := range []struct {
					hit bool
					n   *atomic.Int64
				}{{s.failed, &failed}, {s.listed, &listed}, {s.opened, &opened}, {s.read, &read}} {
					if c.hit {
						c.n.Add(1)
					}
				}
			}
		}()
	}
	time.Sleep(time.Second)
	w.admin.must("PATCH", path, map[string]any{"active": true}, nil, 200)
	waitFor(t, "the reactivation to reconcile", func() bool {
		var pending bool
		return db.QueryRow(ctx, `SELECT acl_pending FROM mailboxes WHERE id = $1`, shared.ID).Scan(&pending) == nil && !pending
	})
	time.Sleep(time.Second)
	stop.Store(true)
	wg.Wait()
	if attempts.Load() < 20 || failed.Load() > 0 {
		t.Fatalf("%d sign-in attempts spanned the activation, %d of them failed to connect; the window was not exercised", attempts.Load(), failed.Load())
	}
	if listed.Load()+opened.Load()+read.Load() != 0 {
		t.Fatalf("a removed member listed the mailbox %d times, opened the secret folder %d times and read it %d times during reactivation", listed.Load(), opened.Load(), read.Load())
	}
	state("after reactivation", true, false)
	a, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer a.Close()
	if !slices.Contains(imapNames(t, a), secret) {
		t.Fatal("the remaining member lost the mailbox")
	}
	if err := tryAppend(a, inbox, rfc822(alice, shared.Address, "after", "x")); err == nil {
		t.Fatal("a member downgraded to read while inactive can append after reactivation")
	}
}

func TestFailedKicksAreRetriedDurably(t *testing.T) {
	db := testDB(t)
	doveadm := dovecotProxy(t, "doveadm")
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliceID := userID(t, w.alice)
	ctx := context.Background()
	alive := func(step string, held ...*imapclient.Client) {
		t.Helper()
		for _, c := range held {
			if err := c.Noop().Wait(); err != nil {
				t.Fatalf("%s: the held session ended although doveadm was unreachable: %v", step, err)
			}
		}
	}
	pending := func(step string, identities ...string) {
		t.Helper()
		for _, identity := range identities {
			waitFor(t, step+": a failed kick for "+identity, func() bool {
				var n int
				return db.QueryRow(ctx, `SELECT count(*) FROM mail_kicks WHERE identity = $1 AND attempts >= 1 AND last_error LIKE '%doveadm kick%' AND next_attempt_at > now()`, identity).Scan(&n) == nil && n == 1
			})
		}
		doveadm.restore()
		if _, err := db.Exec(ctx, `UPDATE mail_kicks SET next_attempt_at = now() WHERE identity = ANY($1)`, identities); err != nil {
			t.Fatalf("%s: make the kicks due: %v", step, err)
		}
	}
	drained := func(step string, identities []string, held ...*imapclient.Client) {
		t.Helper()
		for _, c := range held {
			waitFor(t, step+": the worker to end the held session", func() bool { return c.Noop().Wait() != nil })
			c.Close()
		}
		waitFor(t, step+": the pending kicks to be removed", func() bool {
			var n int
			return db.QueryRow(ctx, `SELECT count(*) FROM mail_kicks WHERE identity = ANY($1)`, identities).Scan(&n) == nil && n == 0
		})
	}
	selected := func(user, pass, folder string) *imapclient.Client {
		t.Helper()
		ic, err := imapLogin(t, user, pass)
		if err != nil {
			t.Fatalf("imap login as %s: %v", user, err)
		}
		if err := tryAppend(ic, folder, rfc822(alice, user, "seed "+randomSuffix(), "x")); err != nil {
			t.Fatalf("append to %s as %s: %v", folder, user, err)
		}
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			t.Fatalf("select %s as %s: %v", folder, user, err)
		}
		return ic
	}

	var ap struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "laptop", "mailboxId": w.aliceMailbox}, &ap, 201)
	own := selected(alice, ap.Password, "INBOX")
	doveadm.cut()
	w.alice.must("DELETE", "/me/app-passwords/"+ap.ID, nil, nil, 204)
	if ic, err := imapLogin(t, alice, ap.Password); err == nil {
		ic.Close()
		t.Fatal("the revoked app password still signs in")
	}
	alive("direct revocation", own)
	pending("direct revocation", alice)
	drained("direct revocation", []string{alice}, own)

	setDelegate(t, w.admin, w.bobMailbox, map[string]any{"email": alice, "access": "full", "sendAs": true})
	delegated := createAppPassword(t, w.alice, w.bobMailbox)
	asBob := selected(bob, delegated, "INBOX")
	doveadm.cut()
	w.admin.must("DELETE", "/mailboxes/"+w.bobMailbox+"/delegates/"+aliceID, nil, nil, 204)
	if ic, err := imapLogin(t, bob, delegated); err == nil {
		ic.Close()
		t.Fatal("the delegated app password still signs in")
	}
	alive("delegate removal", asBob)
	pending("delegate removal", alice, bob)
	drained("delegate removal", []string{alice, bob}, asBob)

	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	path := "/mailboxes/" + shared.ID
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	member := selected(alice, w.aliceAppPassword, inbox)
	doveadm.cut()
	w.admin.must("PATCH", path, map[string]any{"active": false}, nil, 200)
	alive("disable", member)
	var active, aclPending bool
	if err := db.QueryRow(ctx, `SELECT active, acl_pending FROM mailboxes WHERE id = $1`, shared.ID).Scan(&active, &aclPending); err != nil || active || aclPending {
		t.Fatalf("a disable with doveadm unreachable did not strip and close the mailbox (active=%v pending=%v err=%v)", active, aclPending, err)
	}
	pending("disable", alice, shared.Address)
	drained("disable", []string{alice, shared.Address}, member)
	w.admin.must("PATCH", path, map[string]any{"active": true}, nil, 200)
	if err := db.QueryRow(ctx, `SELECT acl_pending FROM mailboxes WHERE id = $1`, shared.ID).Scan(&aclPending); err != nil || aclPending {
		t.Fatalf("reactivation left the mailbox pending (pending=%v, err=%v)", aclPending, err)
	}

	member = selected(alice, w.aliceAppPassword, inbox)
	doveadm.cut()
	w.admin.must("DELETE", path, nil, nil, 204)
	alive("delete", member)
	pending("delete", alice, shared.Address)
	drained("delete", []string{alice, shared.Address}, member)

	setDelegate(t, w.admin, w.bobMailbox, map[string]any{"email": alice, "access": "full", "sendAs": true})
	delegated = createAppPassword(t, w.alice, w.bobMailbox)
	shared = createSharedMailbox(t, w)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	member = selected(alice, w.aliceAppPassword, "Shared/"+shared.Address)
	asBob = selected(bob, delegated, "INBOX")
	doveadm.cut()
	w.admin.must("POST", "/mailboxes/"+w.aliceMailbox+"/convert", map[string]string{"kind": "shared"}, nil, 200)
	alive("conversion", member, asBob)
	pending("conversion", alice, bob)
	for _, id := range []string{w.aliceMailbox, shared.ID} {
		var p bool
		if err := db.QueryRow(ctx, `SELECT acl_pending FROM mailboxes WHERE id = $1`, id).Scan(&p); err != nil || p {
			t.Fatalf("the conversion did not reconcile %s before answering (pending=%v, err=%v)", id, p, err)
		}
	}
	drained("conversion", []string{alice, bob}, member, asBob)
}

func TestWildcardIdentitiesAreRejected(t *testing.T) {
	db := testDB(t)
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	f, slug, providerID := linkedIssuer(t, w, alice)
	var scim struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/admin/identity-providers/"+providerID+"/scim-token", nil, &scim, 200)
	halo := newClient(t)
	halo.bearer = scim.Token
	ctx := context.Background()
	for _, email := range []string{"probe*@" + w.domain, "probe?@" + w.domain} {
		var e struct {
			ScimType string `json:"scimType"`
		}
		body, _ := json.Marshal(map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": email, "externalId": "wild-" + randomSuffix(), "emails": []map[string]any{{"value": email, "primary": true}}, "active": true})
		halo.mustRaw("POST", "/scim/"+slug+"/Users", "application/json", string(body), &e, 400)
		if e.ScimType != "invalidValue" {
			t.Fatalf("SCIM rejected %s with %q, want invalidValue", email, e.ScimType)
		}
		f.subject, f.email = "wild-"+randomSuffix(), email
		browser, callback, _ := oidcAuthorize(t, f, slug)
		resp, err := browser.http.Get(callback)
		if err != nil || !strings.Contains(resp.Header.Get("Location"), "error=not_provisioned") {
			t.Fatalf("OIDC auto-provisioning accepted %s: %v %v", email, err, resp)
		}
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM users WHERE email LIKE $1`, "probe%@"+w.domain).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d wildcard users exist (err=%v)", n, err)
	}
	for _, stmt := range []string{`INSERT INTO users (email) VALUES ('raw*@' || $1)`, `INSERT INTO mail_kicks (identity) VALUES ('raw?@' || $1)`} {
		if _, err := db.Exec(ctx, stmt, w.domain); err == nil {
			t.Fatalf("the database accepted a wildcard identity: %s", stmt)
		}
	}

	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	other, err := imapLogin(t, bob, bobPass)
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer other.Close()
	var ap struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "laptop", "mailboxId": w.aliceMailbox}, &ap, 201)
	own, err := imapLogin(t, alice, ap.Password)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer own.Close()
	w.alice.must("DELETE", "/me/app-passwords/"+ap.ID, nil, nil, 204)
	waitFor(t, "alice's session to end", func() bool { return own.Noop().Wait() != nil })
	if err := other.Noop().Wait(); err != nil {
		t.Fatalf("a kick for %s ended an unrelated session: %v", alice, err)
	}
}

func TestAccessWorkersAreIndependent(t *testing.T) {
	db := testDB(t)
	imapProxy, doveadm := dovecotProxy(t, "imap"), dovecotProxy(t, "doveadm")
	w := setupWorld(t)
	alice := "alice@" + w.domain
	shared := createSharedMailbox(t, w)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	ctx := context.Background()
	var ap struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "laptop", "mailboxId": w.aliceMailbox}, &ap, 201)
	held, err := imapLogin(t, alice, ap.Password)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer held.Close()
	kicks := func(where string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM mail_kicks WHERE identity = $1 AND `+where, alice).Scan(&n); err != nil {
			t.Fatalf("read mail_kicks: %v", err)
		}
		return n
	}

	imapProxy.hold(0)
	if _, err := db.Exec(ctx, `UPDATE mailboxes SET acl_pending = true, acl_generation = acl_generation + 1, acl_pending_since = now() - interval '10 minutes' WHERE id = $1`, shared.ID); err != nil {
		t.Fatalf("mark the mailbox pending: %v", err)
	}
	doveadm.cut()
	w.alice.must("DELETE", "/me/app-passwords/"+ap.ID, nil, nil, 204)
	waitFor(t, "the kick to fail against the cut doveadm path", func() bool { return kicks("attempts >= 1") == 1 })
	if err := held.Noop().Wait(); err != nil {
		t.Fatalf("the held session ended although doveadm was unreachable: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE mail_kicks SET created_at = now() - interval '10 minutes' WHERE identity = $1`, alice); err != nil {
		t.Fatalf("age the kick: %v", err)
	}
	waitFor(t, "the kick backlog gauge to pass the alert threshold while delivery fails", func() bool { return gauge(t, "aether_mail_kick_backlog_seconds") >= 600 })
	waitFor(t, "the ACL backlog gauge to pass the alert threshold while reconciliation stalls", func() bool { return gauge(t, "aether_mail_acl_backlog_seconds") >= 600 })

	doveadm.restore()
	if _, err := db.Exec(ctx, `UPDATE mail_kicks SET next_attempt_at = now() WHERE identity = $1`, alice); err != nil {
		t.Fatalf("make the kick due: %v", err)
	}
	waitFor(t, "the kick worker to end the session while the ACL worker is stalled", func() bool { return held.Noop().Wait() != nil })
	waitFor(t, "the kick row to drain", func() bool { return kicks("true") == 0 })
	waitFor(t, "the kick gauge to return to zero while the ACL gauge still reports the stall", func() bool {
		return gauge(t, "aether_mail_kick_backlog_seconds") == 0 && gauge(t, "aether_mail_acl_backlog_seconds") >= 600
	})

	imapProxy.restore()
	waitUpTo(t, 90*time.Second, "the ACL worker to recover", func() bool {
		var pending bool
		return db.QueryRow(ctx, `SELECT acl_pending FROM mailboxes WHERE id = $1`, shared.ID).Scan(&pending) == nil && !pending
	})
	waitFor(t, "the ACL gauge to return to zero", func() bool { return gauge(t, "aether_mail_acl_backlog_seconds") == 0 })
}

func TestKicksCoalescePerIdentity(t *testing.T) {
	db := testDB(t)
	doveadm := dovecotProxy(t, "doveadm")
	w := setupWorld(t)
	alice := "alice@" + w.domain
	aliceID := userID(t, w.alice)
	ctx := context.Background()
	row := func() (int, int64) {
		t.Helper()
		var n int
		var generation int64
		if err := db.QueryRow(ctx, `SELECT count(*), coalesce(max(generation), -1) FROM mail_kicks WHERE identity = $1`, alice).Scan(&n, &generation); err != nil {
			t.Fatalf("read mail_kicks: %v", err)
		}
		return n, generation
	}
	issue := func() string {
		t.Helper()
		var ap struct {
			ID string `json:"id"`
		}
		w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "laptop", "mailboxId": w.aliceMailbox}, &ap, 201)
		return ap.ID
	}
	revoke := func(id string) {
		t.Helper()
		start := time.Now()
		w.alice.must("DELETE", "/me/app-passwords/"+id, nil, nil, 204)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("revoking an app password took %s", d)
		}
	}

	held, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer held.Close()
	first, second := issue(), issue()
	doveadm.delay(3000)
	revoke(first)
	time.Sleep(time.Second)
	revoke(second)
	time.Sleep(3500 * time.Millisecond)
	if err := held.Noop().Wait(); err == nil {
		t.Fatal("the first kick did not end the held session")
	}
	if n, generation := row(); n != 1 || generation != 1 {
		t.Fatalf("after the first kick completed: %d rows at generation %d, want the second revocation's row at generation 1", n, generation)
	}
	waitFor(t, "the second generation to be kicked and removed", func() bool { n, _ := row(); return n == 0 })
	doveadm.restore()

	doveadm.hold(10000)
	var ids []string
	for range 5 {
		ids = append(ids, issue())
	}
	for _, id := range ids {
		revoke(id)
	}
	setDelegate(t, w.admin, w.bobMailbox, map[string]any{"email": alice, "access": "full", "sendAs": true})
	start := time.Now()
	w.admin.must("DELETE", "/mailboxes/"+w.bobMailbox+"/delegates/"+aliceID, nil, nil, 204)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("removing a delegate took %s while doveadm was stalled", d)
	}
	if n, generation := row(); n != 1 || generation != 5 {
		t.Fatalf("after six revocations against a stalled doveadm: %d rows at generation %d, want one row at generation 5", n, generation)
	}
	doveadm.restore()
	waitUpTo(t, 60*time.Second, "the coalesced kick to drain", func() bool { n, _ := row(); return n == 0 })
}

func TestACLSyncHoldsNoDatabaseTransaction(t *testing.T) {
	db := testDB(t)
	imapProxy := dovecotProxy(t, "imap")
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	ctx := context.Background()
	var teams []sharedMailbox
	for i := range 6 {
		var m sharedMailbox
		w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": fmt.Sprintf("team%d", i), "displayName": "Team", "kind": "shared"}, &m, 201)
		setDelegate(t, w.admin, m.ID, map[string]any{"email": alice, "access": "full"})
		teams = append(teams, m)
	}
	openTransactions := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%'`).Scan(&n); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		return n
	}

	imapProxy.delay(60000)
	codes := make(chan int, len(teams)+1)
	for _, m := range teams {
		go func() { codes <- w.admin.do("PATCH", "/mailboxes/"+m.ID, map[string]any{"active": false}, nil) }()
	}
	go func() {
		codes <- w.admin.do("POST", "/mailboxes/"+teams[0].ID+"/delegates", map[string]any{"email": bob, "access": "read"}, nil)
	}()
	time.Sleep(2 * time.Second)
	if n := openTransactions(); n != 0 {
		t.Fatalf("%d database transactions are open while the ACL work is stalled", n)
	}
	start := time.Now()
	w.bob.must("GET", "/me/mailboxes", nil, nil, 200)
	w.bob.must("POST", "/me/app-passwords", map[string]string{"name": "phone", "mailboxId": w.bobMailbox}, nil, 201)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("unrelated requests took %s while the ACL work was stalled", d)
	}
	imapProxy.restore()
	for range cap(codes) {
		if code := <-codes; code != 200 {
			t.Fatalf("a request that waited for the stalled ACL work answered %d after the mail server recovered", code)
		}
	}
	for _, m := range teams {
		var active bool
		if err := db.QueryRow(ctx, `SELECT active FROM mailboxes WHERE id = $1`, m.ID).Scan(&active); err != nil || active {
			t.Fatalf("%s is still active (err=%v)", m.Address, err)
		}
	}
	a, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	for _, name := range imapNames(t, a) {
		if strings.HasPrefix(name, "Shared/") {
			t.Fatalf("a member still lists %s after the deactivations", name)
		}
	}
	a.Close()
	w.admin.must("PATCH", "/mailboxes/"+teams[0].ID, map[string]any{"active": true}, nil, 200)
	b, err := imapLogin(t, bob, bobPass)
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer b.Close()
	if !slices.Contains(imapNames(t, b), "Shared/"+teams[0].Address) {
		t.Fatal("the membership added during the stalled deactivation was lost")
	}
}

func TestDomainDeletionRequiresNoMailboxes(t *testing.T) {
	db := testDB(t)
	imapProxy := dovecotProxy(t, "imap")
	w := setupWorld(t)
	alice := "alice@" + w.domain
	ctx := context.Background()
	var dom idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/domains", map[string]string{"name": "del" + randomSuffix() + ".test"}, &dom, 201)
	var shared sharedMailbox
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": dom.ID, "localPart": "support", "displayName": "Support", "kind": "shared"}, &shared, 201)
	inbox := "Shared/" + shared.Address
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "full"})
	held, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("alice imap login: %v", err)
	}
	defer held.Close()
	if err := tryAppend(held, inbox, rfc822("customer@external.example", shared.Address, "question", "hello")); err != nil {
		t.Fatalf("member append: %v", err)
	}
	if _, err := held.Select(inbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", inbox, err)
	}
	unchanged := func(step string) {
		t.Helper()
		var active bool
		if err := db.QueryRow(ctx, `SELECT m.active FROM mailboxes m JOIN domains d ON d.id = m.domain_id WHERE m.id = $1 AND d.id = $2 AND d.active AND d.verified_at IS NOT NULL`, shared.ID, dom.ID).Scan(&active); err != nil || !active {
			t.Fatalf("%s: the mailbox or its domain changed (active=%v err=%v)", step, active, err)
		}
		if _, err := held.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect(); err != nil {
			t.Fatalf("%s: the held member session cannot read: %v", step, err)
		}
		if err := tryAppend(held, inbox, rfc822(alice, shared.Address, step, "x")); err != nil {
			t.Fatalf("%s: the held member session cannot write: %v", step, err)
		}
	}

	if code := errorCode(t, w.admin, "DELETE", "/domains/"+dom.ID, nil, 409); code != "domain_not_empty" {
		t.Fatalf("deleting a domain that still has a mailbox: %q, want domain_not_empty", code)
	}
	unchanged("after the refused domain deletion")

	imapProxy.cut()
	if code := errorCode(t, w.admin, "DELETE", "/mailboxes/"+shared.ID, nil, 503); code != "mail_unavailable" {
		t.Fatalf("deleting a shared mailbox with Dovecot unreachable: %q, want mail_unavailable", code)
	}
	imapProxy.restore()
	unchanged("after the refused mailbox deletion")

	start := time.Now()
	w.admin.must("DELETE", "/mailboxes/"+shared.ID, nil, nil, 204)
	waitKicked(t, held, start)
	if err := tryAppend(held, inbox, rfc822(alice, shared.Address, "late", "x")); err == nil {
		t.Fatal("the held session still writes to the deleted mailbox")
	}
	waitFor(t, "the kicks to drain", func() bool {
		var n int
		return db.QueryRow(ctx, `SELECT count(*) FROM mail_kicks WHERE identity IN ($1, $2)`, alice, shared.Address).Scan(&n) == nil && n == 0
	})
	w.admin.must("DELETE", "/domains/"+dom.ID, nil, nil, 204)
	w.admin.must("GET", "/domains/"+dom.ID, nil, nil, 404)
}

func TestACLWorkerSchedulesFairly(t *testing.T) {
	db := testDB(t)
	locker := testDB(t)
	w := setupWorld(t)
	alice := "alice@" + w.domain
	ctx := context.Background()
	create := func(local string) sharedMailbox {
		t.Helper()
		var m sharedMailbox
		w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": local, "displayName": "Team", "kind": "shared"}, &m, 201)
		setDelegate(t, w.admin, m.ID, map[string]any{"email": alice, "access": "full"})
		return m
	}
	reconciled := func(id string) bool {
		var pending bool
		return db.QueryRow(ctx, `SELECT acl_pending FROM mailboxes WHERE id = $1`, id).Scan(&pending) == nil && !pending
	}
	mark := func(id, overdue string) {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE mailboxes SET acl_pending = true, acl_generation = acl_generation + 1, acl_pending_since = coalesce(acl_pending_since, now()), acl_attempts = 0, acl_next_attempt_at = now() - $2::interval WHERE id = $1`, id, overdue); err != nil {
			t.Fatalf("mark %s pending: %v", id, err)
		}
	}

	older, younger := create("older"), create("younger")
	tx, err := locker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	mark(older.ID, "2 minutes")
	if _, err := tx.Exec(ctx, `SELECT 1 FROM mailboxes WHERE id = $1 FOR UPDATE`, older.ID); err != nil {
		t.Fatalf("lock the older mailbox: %v", err)
	}
	mark(younger.ID, "0 seconds")
	waitUpTo(t, 40*time.Second, "the younger due mailbox to be reconciled while an older one is held by another claimer", func() bool { return reconciled(younger.ID) })
	tx.Rollback(ctx)
	waitFor(t, "the older mailbox to be reconciled once released", func() bool { return reconciled(older.ID) })

	var stuck []string
	for i := range 20 {
		stuck = append(stuck, create(fmt.Sprintf("stuck%02d", i)).ID)
	}
	t.Cleanup(func() { db.Exec(ctx, `DELETE FROM mailboxes WHERE id = ANY($1)`, stuck) })
	if _, err := db.Exec(ctx, `UPDATE mailboxes SET address = upper(address) WHERE id = ANY($1)`, stuck); err != nil {
		t.Fatalf("break the master login of the stuck mailboxes: %v", err)
	}
	for _, id := range stuck {
		mark(id, "1 minute")
	}
	valid := create("valid")
	mark(valid.ID, "0 seconds")
	waitUpTo(t, 45*time.Second, "the valid mailbox to be reconciled behind 20 mailboxes whose sync always fails", func() bool { return reconciled(valid.ID) })
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE id = ANY($1) AND acl_pending AND acl_attempts >= 1`, stuck).Scan(&n); err != nil || n != 20 {
		t.Fatalf("%d of the 20 failing mailboxes are pending after at least one attempt (err=%v); if fewer, the upper-case fixture did not fail the master login", n, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE id = ANY($1) AND acl_next_attempt_at > now() + interval '10 seconds'`, stuck).Scan(&n); err != nil || n == 0 {
		t.Fatalf("no failing mailbox is backing off (err=%v)", err)
	}
	if g := gauge(t, "aether_mail_acl_backlog_seconds"); g <= 0 {
		t.Fatalf("the ACL backlog gauge reads %v while 20 mailboxes are pending", g)
	}
}

func TestACLLeaseLossFencesTheWorker(t *testing.T) {
	db := testDB(t)
	imapProxy := dovecotProxy(t, "imap")
	w := setupWorld(t)
	ctx := context.Background()
	shared := createSharedMailbox(t, w)
	inbox := "Shared/" + shared.Address
	type member struct{ email, password string }
	var members []member
	var emails []string
	for i := range 16 {
		local := fmt.Sprintf("m%02d", i)
		email := local + "@" + w.domain
		var mb idOnly
		w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": local, "displayName": local, "password": local + "-integration-pw"}, &mb, 201)
		c := newClient(t)
		c.login(email, local+"-integration-pw")
		members = append(members, member{email, createAppPassword(t, c, mb.ID)})
		emails = append(emails, email)
	}
	withRights := func() int {
		t.Helper()
		n := 0
		for _, m := range members {
			ic, err := imapLogin(t, m.email, m.password)
			if err != nil {
				t.Fatalf("%s imap login: %v", m.email, err)
			}
			if slices.Contains(imapNames(t, ic), inbox) {
				n++
			}
			ic.Close()
		}
		return n
	}
	row := func() (pending, leased bool, generation int64) {
		t.Helper()
		if err := db.QueryRow(ctx, `SELECT acl_pending, acl_lease_token IS NOT NULL, acl_generation FROM mailboxes WHERE id = $1`, shared.ID).Scan(&pending, &leased, &generation); err != nil {
			t.Fatalf("read the mailbox row: %v", err)
		}
		return
	}
	mark := func() {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE mailboxes SET acl_pending = true, acl_generation = acl_generation + 1, acl_pending_since = coalesce(acl_pending_since, now()), acl_attempts = 0, acl_next_attempt_at = now() WHERE id = $1`, shared.ID); err != nil {
			t.Fatalf("mark the mailbox pending: %v", err)
		}
		waitFor(t, "the worker to claim the mailbox", func() bool { _, leased, _ := row(); return leased })
	}
	steal := func(lease string) int64 {
		t.Helper()
		var generation int64
		if err := db.QueryRow(ctx, `UPDATE mailboxes SET acl_lease_token = gen_random_uuid(), acl_lease_until = now() + $2::interval, acl_pending = true, acl_generation = acl_generation + 1, acl_pending_since = coalesce(acl_pending_since, now()), acl_attempts = 0, acl_next_attempt_at = now() WHERE id = $1 RETURNING acl_generation`, shared.ID, lease).Scan(&generation); err != nil {
			t.Fatalf("take over the lease: %v", err)
		}
		return generation
	}
	settled := func(step string) {
		t.Helper()
		waitUpTo(t, 3*time.Minute, step+": the mailbox to be reconciled and released", func() bool { pending, leased, _ := row(); return !pending && !leased })
	}

	if _, err := db.Exec(ctx, `INSERT INTO mailbox_delegates (mailbox_id, user_id, access) SELECT $1::uuid, id, 'full' FROM users WHERE email = ANY($2)`, shared.ID, emails); err != nil {
		t.Fatalf("add the members: %v", err)
	}
	imapProxy.delay(3000)
	mark()
	time.Sleep(30 * time.Second)
	if _, err := db.Exec(ctx, `DELETE FROM mailbox_delegates WHERE mailbox_id = $1`, shared.ID); err != nil {
		t.Fatalf("remove the members: %v", err)
	}
	taken := steal("75 seconds")
	time.Sleep(50 * time.Second)
	if n := withRights(); n > 8 {
		t.Fatalf("%d of 16 removed members hold rights after the takeover: the fenced-out worker kept writing generation-1 entries", n)
	}
	if _, _, generation := row(); generation != taken+1 {
		t.Fatalf("generation %d after the takeover, want %d: the fenced-out worker did not re-mark the mailbox", generation, taken+1)
	}
	imapProxy.restore()
	settled("after the takeover")
	if n := withRights(); n != 0 {
		t.Fatalf("%d removed members still hold rights after the successor's pass", n)
	}

	imapProxy.delay(3000)
	mark()
	taken = steal("45 seconds")
	waitUpTo(t, 90*time.Second, "the worker to refuse its stable pass after losing the lease", func() bool { _, _, generation := row(); return generation == taken+1 })
	imapProxy.restore()
	settled("after the refused stable pass")
	if pending, _, generation := row(); pending || generation != taken+1 {
		t.Fatalf("after the refused stable pass: pending=%v generation=%d, want reconciled at generation %d", pending, generation, taken+1)
	}
}

func TestACLChurnDoesNotPinTheQueue(t *testing.T) {
	db := testDB(t)
	churner := testDB(t)
	imapProxy := dovecotProxy(t, "imap")
	w := setupWorld(t)
	alice := "alice@" + w.domain
	ctx := context.Background()
	create := func(local string) sharedMailbox {
		t.Helper()
		var m sharedMailbox
		w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": local, "displayName": "Team", "kind": "shared"}, &m, 201)
		setDelegate(t, w.admin, m.ID, map[string]any{"email": alice, "access": "full"})
		return m
	}
	row := func(id string) (pending bool, token *string, attempts int, due time.Time) {
		t.Helper()
		if err := db.QueryRow(ctx, `SELECT acl_pending, acl_lease_token::text, acl_attempts, acl_next_attempt_at FROM mailboxes WHERE id = $1`, id).Scan(&pending, &token, &attempts, &due); err != nil {
			t.Fatalf("read the mailbox row: %v", err)
		}
		return
	}
	settled := func(id string) bool { pending, token, _, _ := row(id); return !pending && token == nil }
	mark := func(id, overdue string) {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE mailboxes SET acl_pending = true, acl_generation = acl_generation + 1, acl_pending_since = coalesce(acl_pending_since, now()), acl_attempts = 0, acl_next_attempt_at = now() - $2::interval WHERE id = $1`, id, overdue); err != nil {
			t.Fatalf("mark %s pending: %v", id, err)
		}
	}
	const change = `UPDATE mailboxes SET acl_pending = true, acl_generation = acl_generation + 1, acl_pending_since = coalesce(acl_pending_since, now()), acl_attempts = 0, acl_next_attempt_at = CASE WHEN acl_pending THEN least(acl_next_attempt_at, now()) ELSE now() END WHERE id = $1`

	churned, later := create("churned"), create("later")
	waitFor(t, "both mailboxes to be reconciled", func() bool { return settled(churned.ID) && settled(later.ID) })

	imapProxy.delay(1000)
	mark(churned.ID, "3 minutes")
	mark(later.ID, "0 seconds")
	stop := make(chan struct{})
	var changes atomic.Int32
	go func() {
		seen := ""
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			var token *string
			if err := churner.QueryRow(ctx, `SELECT acl_lease_token::text FROM mailboxes WHERE id = $1`, churned.ID).Scan(&token); err != nil || token == nil || *token == seen {
				continue
			}
			seen = *token
			if _, err := churner.Exec(ctx, change, churned.ID); err == nil {
				changes.Add(1)
			}
		}
	}()
	waitUpTo(t, 75*time.Second, "the later mailbox to be reconciled behind a mailbox that changes during every attempt", func() bool { return settled(later.ID) })
	if pending, _, _, _ := row(churned.ID); !pending || changes.Load() == 0 {
		t.Fatalf("the churned mailbox is pending=%v after %d changes during its attempts, want it still pending after at least one", pending, changes.Load())
	}
	close(stop)
	imapProxy.restore()
	waitUpTo(t, 90*time.Second, "the churned mailbox to be reconciled once it stops changing", func() bool { return settled(churned.ID) })

	imapProxy.hold(0)
	mark(churned.ID, "3 minutes")
	waitFor(t, "the worker to claim the churned mailbox", func() bool { _, token, _, _ := row(churned.ID); return token != nil })
	changed := time.Now()
	if _, err := db.Exec(ctx, change, churned.ID); err != nil {
		t.Fatalf("change the churned mailbox during a stalled attempt: %v", err)
	}
	waitUpTo(t, 60*time.Second, "the stalled attempt to fail and release the lease", func() bool { _, _, attempts, _ := row(churned.ID); return attempts > 0 })
	pending, _, attempts, due := row(churned.ID)
	if !pending || attempts != 1 {
		t.Fatalf("after a failed attempt at a changed generation: pending=%v attempts=%d, want pending with one attempt", pending, attempts)
	}
	if due.Before(changed) || due.After(time.Now().Add(5*time.Second)) {
		t.Fatalf("after a failed attempt at a changed generation the next attempt is due at %s (changed at %s, now %s): the row kept its old place or backed off", due.Format(time.RFC3339Nano), changed.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano))
	}
	imapProxy.restore()
	waitUpTo(t, 90*time.Second, "the churned mailbox to be reconciled after the stall", func() bool { return settled(churned.ID) })
}

func TestHandlerACLWorkIsBounded(t *testing.T) {
	db := testDB(t)
	imapProxy := dovecotProxy(t, "imap")
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	ctx := context.Background()
	create := func(local string) sharedMailbox {
		t.Helper()
		var m sharedMailbox
		w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": local, "displayName": "Team", "kind": "shared"}, &m, 201)
		return m
	}
	support, ops, sales := create("support"), create("ops"), create("sales")
	aliceID := setDelegate(t, w.admin, support.ID, map[string]any{"email": alice, "access": "full"}).UserID
	var bobID string
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, bob).Scan(&bobID); err != nil {
		t.Fatalf("look up bob: %v", err)
	}
	row := func(id string) (pending, active bool, token *string) {
		t.Helper()
		if err := db.QueryRow(ctx, `SELECT acl_pending, active, acl_lease_token::text FROM mailboxes WHERE id = $1`, id).Scan(&pending, &active, &token); err != nil {
			t.Fatalf("read the mailbox row: %v", err)
		}
		return
	}
	settled := func(ids ...string) bool {
		for _, id := range ids {
			if pending, _, token := row(id); pending || token != nil {
				return false
			}
		}
		return true
	}
	members := func(id string) []string {
		t.Helper()
		rows, err := db.Query(ctx, `SELECT u.email FROM mailbox_delegates md JOIN users u ON u.id = md.user_id WHERE md.mailbox_id = $1 ORDER BY u.email`, id)
		if err != nil {
			t.Fatalf("read the members: %v", err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read the members: %v", err)
		}
		return out
	}
	lists := func(email, password, address string) bool {
		t.Helper()
		ic, err := imapLogin(t, email, password)
		if err != nil {
			t.Fatalf("%s imap login: %v", email, err)
		}
		defer ic.Close()
		return slices.Contains(imapNames(t, ic), "Shared/"+address)
	}
	patient := *w.admin
	patient.http = &http.Client{Jar: w.admin.http.Jar, Timeout: 2 * time.Minute}
	type answer struct {
		what string
		code int
		took time.Duration
	}
	run := func(within time.Duration, want map[string]int, calls map[string]func() int) {
		t.Helper()
		out := make(chan answer, len(calls))
		for what, call := range calls {
			go func() {
				start := time.Now()
				out <- answer{what, call(), time.Since(start)}
			}()
		}
		time.Sleep(2 * time.Second)
		start := time.Now()
		w.admin.must("GET", "/organizations/"+w.orgID+"/mailboxes", nil, nil, 200)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("an unrelated request took %s while ACL work was blocked", d)
		}
		for range len(calls) {
			a := <-out
			if a.code != want[a.what] || a.took > within {
				t.Fatalf("%s answered %d after %s, want %d within %s", a.what, a.code, a.took, want[a.what], within)
			}
		}
	}
	call := func(method, path string, body any) func() int {
		return func() int { return patient.do(method, path, body, nil) }
	}

	waitFor(t, "the mailboxes to be reconciled", func() bool { return settled(support.ID, ops.ID, sales.ID) })
	var phantom string
	if err := db.QueryRow(ctx, `UPDATE mailboxes SET acl_lease_token = gen_random_uuid(), acl_lease_until = now() + interval '10 minutes' WHERE id = $1 RETURNING acl_lease_token::text`, support.ID).Scan(&phantom); err != nil {
		t.Fatalf("hold the lease: %v", err)
	}
	run(90*time.Second,
		map[string]int{"adding a member": 200, "removing a member": 204, "deactivating": 503, "deleting": 503},
		map[string]func() int{
			"adding a member":   call("POST", "/mailboxes/"+support.ID+"/delegates", map[string]any{"email": bob, "access": "full"}),
			"removing a member": call("DELETE", "/mailboxes/"+support.ID+"/delegates/"+aliceID, nil),
			"deactivating":      call("PATCH", "/mailboxes/"+support.ID, map[string]any{"active": false}),
			"deleting":          call("DELETE", "/mailboxes/"+support.ID, nil),
		})
	if pending, active, token := row(support.ID); !pending || !active || token == nil || *token != phantom {
		t.Fatalf("after the bounded handlers: pending=%v active=%v lease untouched=%v, want pending, active and the held lease untouched", pending, active, token != nil && *token == phantom)
	}
	if got := members(support.ID); !slices.Equal(got, []string{bob}) {
		t.Fatalf("members after the bounded handlers: %v, want [%s]", got, bob)
	}
	if _, err := db.Exec(ctx, `UPDATE mailboxes SET acl_lease_token = NULL, acl_lease_until = NULL WHERE id = $1`, support.ID); err != nil {
		t.Fatalf("release the held lease: %v", err)
	}
	waitUpTo(t, 60*time.Second, "the worker to apply the membership changes made while the lease was held", func() bool { return settled(support.ID) })
	if !lists(bob, bobPass, support.Address) || lists(alice, w.aliceAppPassword, support.Address) {
		t.Fatal("the membership changes made while the lease was held were not applied by the worker")
	}

	imapProxy.cut()
	run(15*time.Second,
		map[string]int{"adding to ops": 200, "adding to sales": 200, "removing from support": 204, "deactivating ops": 503, "deleting sales": 503},
		map[string]func() int{
			"adding to ops":         call("POST", "/mailboxes/"+ops.ID+"/delegates", map[string]any{"email": bob, "access": "full"}),
			"adding to sales":       call("POST", "/mailboxes/"+sales.ID+"/delegates", map[string]any{"email": bob, "access": "read"}),
			"removing from support": call("DELETE", "/mailboxes/"+support.ID+"/delegates/"+bobID, nil),
			"deactivating ops":      call("PATCH", "/mailboxes/"+ops.ID, map[string]any{"active": false}),
			"deleting sales":        call("DELETE", "/mailboxes/"+sales.ID, nil),
		})
	for _, m := range []sharedMailbox{support, ops, sales} {
		if pending, active, _ := row(m.ID); !pending || !active {
			t.Fatalf("after the changes with Dovecot unreachable: %s pending=%v active=%v, want pending and active", m.Address, pending, active)
		}
	}
	imapProxy.restore()
	waitUpTo(t, 90*time.Second, "the worker to reconcile the mailboxes changed while Dovecot was unreachable", func() bool { return settled(support.ID, ops.ID, sales.ID) })
	if lists(bob, bobPass, support.Address) || !lists(bob, bobPass, ops.Address) || !lists(bob, bobPass, sales.Address) {
		t.Fatal("the changes made while Dovecot was unreachable were not applied by the worker")
	}
}

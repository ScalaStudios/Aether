//go:build integration

package integration

import (
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

type accessLog struct {
	Summary []struct {
		ActorEmail string `json:"actorEmail"`
		ActorRole  string `json:"actorRole"`
		Events     int    `json:"events"`
	} `json:"summary"`
	Events []struct {
		ActorEmail string         `json:"actorEmail"`
		ActorRole  string         `json:"actorRole"`
		Action     string         `json:"action"`
		Folder     string         `json:"folder"`
		Subject    string         `json:"subject"`
		MessageID  string         `json:"messageId"`
		Client     string         `json:"client"`
		Metadata   map[string]any `json:"metadata"`
	} `json:"events"`
}

func (l accessLog) has(actor, action, client string, match func(subject, folder string) bool) bool {
	for _, e := range l.Events {
		if e.ActorEmail == actor && e.Action == action && e.Client == client && match(e.Subject, e.Folder) {
			return true
		}
	}
	return false
}

func anyEntry(string, string) bool { return true }

func TestMailboxAccessAudit(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	settings := "/organizations/" + w.orgID + "/mailbox-audit"
	report := "/mailboxes/" + w.bobMailbox + "/access-log"

	var initial struct {
		Enabled       bool `json:"enabled"`
		RetentionDays int  `json:"retentionDays"`
	}
	w.admin.must("GET", settings, nil, &initial, 200)
	if initial.Enabled || initial.RetentionDays != 180 {
		t.Fatalf("unexpected defaults: %+v", initial)
	}
	if code := errorCode(t, w.admin, "PUT", settings, map[string]any{"enabled": true, "retentionDays": 0}, 422); code != "invalid_retention" {
		t.Fatalf("retention 0 answered %q", code)
	}
	if code := w.bob.do("PUT", settings, map[string]any{"enabled": false, "retentionDays": 30}, nil); code != 403 {
		t.Fatalf("a member changed the audit settings: %d", code)
	}
	w.admin.must("PUT", settings, map[string]any{"enabled": true, "logReads": true, "logOwnerDeletes": false, "ownersCanView": false, "retentionDays": 30}, nil, 200)

	w.admin.must("POST", "/mailboxes/"+w.bobMailbox+"/delegates", map[string]any{"email": alice, "sendAs": true}, nil, 200)
	subject := "audited " + randomSuffix()
	w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "hello"}, nil, 202)
	var uid uint32
	waitFor(t, "bob to receive the message", func() bool { uid = findInAPI(w.alice, w.bobMailbox, "INBOX", subject); return uid != 0 })
	findInAPI(w.bob, w.bobMailbox, "INBOX", subject)
	w.alice.must("GET", "/mailboxes/"+w.bobMailbox+"/messages/"+itoaUID(uid)+"?folder=INBOX", nil, nil, 200)
	w.alice.must("DELETE", "/mailboxes/"+w.bobMailbox+"/messages/"+itoaUID(uid)+"?folder=INBOX", nil, nil, 204)
	sent := "on behalf " + randomSuffix()
	w.alice.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{"to": []map[string]string{{"address": alice}}, "subject": sent, "text": "x"}, nil, 202)

	var log accessLog
	w.admin.must("GET", report, nil, &log, 200)
	bySubject := func(s, _ string) bool { return s == subject }
	for _, action := range []string{"message.read", "message.delete"} {
		if !log.has(alice, action, "web", bySubject) {
			t.Fatalf("no %s entry for the delegate: %+v", action, log.Events)
		}
	}
	if !log.has(alice, "folder.open", "web", func(_, f string) bool { return f == "INBOX" }) {
		t.Fatalf("no folder.open entry: %+v", log.Events)
	}
	if !log.has(alice, "message.send_as", "web", func(s, _ string) bool { return s == sent }) {
		t.Fatalf("no send_as entry: %+v", log.Events)
	}
	for _, e := range log.Events {
		if e.ActorEmail == bob || e.ActorRole != "delegate" {
			t.Fatalf("owner activity or wrong role recorded: %+v", e)
		}
	}
	if len(log.Summary) != 1 || log.Summary[0].ActorEmail != alice {
		t.Fatalf("summary should list only alice: %+v", log.Summary)
	}

	if code := w.bob.do("GET", report, nil, nil); code != 403 {
		t.Fatalf("the owner read the report while owners cannot view it: %d", code)
	}
	if code := w.alice.do("GET", report, nil, nil); code != 403 {
		t.Fatalf("a delegate read the report: %d", code)
	}
	w.admin.must("PUT", settings, map[string]any{"enabled": true, "logReads": true, "logOwnerDeletes": false, "ownersCanView": true, "retentionDays": 30}, nil, 200)
	w.bob.must("GET", report, nil, nil, 200)

	imapSubject := "over imap " + randomSuffix()
	w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": imapSubject, "text": "hello"}, nil, 202)
	waitFor(t, "bob to receive the IMAP test message", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", imapSubject) != 0 })

	pass := createAppPassword(t, w.alice, w.bobMailbox)
	ic, err := imapLogin(t, bob, pass)
	if err != nil {
		t.Fatalf("delegate app password login: %v", err)
	}
	if imapFindSubject(t, ic, "INBOX", imapSubject) == nil {
		t.Fatal("the delegate could not fetch the message over IMAP")
	}
	ic.Logout().Wait()
	ic.Close()
	waitFor(t, "the delegate's IMAP read to be recorded", func() bool {
		var l accessLog
		w.admin.must("GET", report, nil, &l, 200)
		return l.has(alice, "message.read", "app_password", func(s, f string) bool { return s == imapSubject && f == "INBOX" })
	})
	bySubjectIn := func(subject, folder string) func(string, string) bool {
		return func(s, f string) bool { return s == subject && f == folder }
	}
	waitRecorded := func(what, mailbox, actor, action, client string, match func(string, string) bool) {
		t.Helper()
		waitFor(t, what, func() bool {
			var l accessLog
			w.admin.must("GET", "/mailboxes/"+mailbox+"/access-log", nil, &l, 200)
			return l.has(actor, action, client, match)
		})
	}

	ic, err = imapLogin(t, bob, pass)
	if err != nil {
		t.Fatalf("delegate app password login: %v", err)
	}
	if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select INBOX: %v", err)
	}
	found, err := ic.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: imapSubject}}}, nil).Wait()
	if err != nil || len(found.AllUIDs()) != 1 {
		t.Fatalf("search INBOX: %v %v", found, err)
	}
	original := imap.UIDSetNum(found.AllUIDs()[0])
	copied, err := ic.Copy(original, "Archive").Wait()
	if err != nil {
		t.Fatalf("copy to Archive: %v", err)
	}
	waitRecorded("the IMAP copy to be recorded with its subject", w.bobMailbox, alice, "message.copy", "app_password", bySubjectIn(imapSubject, "INBOX"))
	if _, err := ic.Select("Archive", nil).Wait(); err != nil {
		t.Fatalf("select Archive: %v", err)
	}
	dest := copied.DestUIDs
	if err := ic.Store(dest, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
		t.Fatalf("flag the copy deleted: %v", err)
	}
	if err := ic.UIDExpunge(dest).Close(); err != nil {
		t.Fatalf("expunge the copy: %v", err)
	}
	waitRecorded("the IMAP expunge to be recorded with the copy's subject", w.bobMailbox, alice, "message.delete", "app_password", bySubjectIn(imapSubject, "Archive"))
	if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select INBOX: %v", err)
	}
	if _, err := ic.Move(original, "Trash").Wait(); err != nil {
		t.Fatalf("move to Trash: %v", err)
	}
	waitRecorded("the move to Trash to be recorded as a deletion with its subject", w.bobMailbox, alice, "message.delete", "app_password", bySubjectIn(imapSubject, "INBOX"))
	ic.Logout().Wait()
	ic.Close()

	submission := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	smtpSubject := "delegate smtp " + randomSuffix()
	if err := submit(t, submission, bob, pass, bob, []string{alice}, rfc822(bob, alice, smtpSubject, "x"), false); err != nil {
		t.Fatalf("delegate app password SMTP send as the owner: %v", err)
	}
	waitRecorded("the delegate's SMTP send to be recorded", w.bobMailbox, alice, "message.send_as", "smtp", bySubjectIn(smtpSubject, ""))

	var owned struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	w.bob.must("POST", "/me/app-passwords", map[string]string{"name": "own", "mailboxId": w.bobMailbox}, &owned, 201)
	own, err := imapLogin(t, bob, owned.Password)
	if err != nil {
		t.Fatalf("owner login: %v", err)
	}
	imapFindSubject(t, own, "INBOX", imapSubject)
	own.Logout().Wait()
	own.Close()

	var shared sharedMailbox
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "desk", "displayName": "Desk", "kind": "shared"}, &shared, 201)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": bob})
	deskSubject := "for desk " + randomSuffix()
	w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{"to": []map[string]string{{"address": shared.Address}}, "subject": deskSubject, "text": "hello"}, nil, 202)
	waitFor(t, "desk to receive mail", func() bool { return findInAPI(w.bob, shared.ID, "INBOX", deskSubject) != 0 })
	folder := "Shared/" + shared.Address
	member := func() bool {
		c, err := imapLogin(t, bob, owned.Password)
		if err != nil {
			t.Fatalf("member login: %v", err)
		}
		defer c.Close()
		ok := slices.Contains(imapNames(t, c), folder) && imapFindSubject(t, c, folder, deskSubject) != nil
		c.Logout().Wait()
		return ok
	}
	waitFor(t, "the member to see the shared folder", member)
	waitRecorded("the member's IMAP read to be recorded", shared.ID, bob, "message.read", "imap", bySubjectIn(deskSubject, "INBOX"))

	setDelegate(t, w.admin, shared.ID, map[string]any{"email": bob, "sendAs": true})
	deskReply := "desk smtp " + randomSuffix()
	if err := submit(t, submission, bob, owned.Password, shared.Address, []string{alice}, rfc822(shared.Address, alice, deskReply, "x"), false); err != nil {
		t.Fatalf("member SMTP send as the shared mailbox: %v", err)
	}
	waitRecorded("the member's SMTP send to be recorded", shared.ID, bob, "message.send_as", "smtp", bySubjectIn(deskReply, ""))
	var desk accessLog
	w.admin.must("GET", "/mailboxes/"+shared.ID+"/access-log", nil, &desk, 200)
	for _, e := range desk.Events {
		if e.Action == "message.send_as" && e.Subject == deskReply && !strings.HasSuffix(e.MessageID, "@integration.test>") {
			t.Fatalf("SMTP send entry has no Message-ID: %+v", e)
		}
	}

	var final accessLog
	w.admin.must("GET", report, nil, &final, 200)
	for _, e := range final.Events {
		if e.ActorEmail == bob {
			t.Fatalf("the owner's own IMAP session was recorded: %+v", e)
		}
	}
	w.admin.must("GET", report+"?since=2000-01-01&until=2000-01-02", nil, &final, 200)
	if len(final.Events) != 0 || len(final.Summary) != 0 {
		t.Fatalf("date filter returned entries: %+v", final)
	}
	if code := errorCode(t, w.admin, "GET", report+"?since=yesterday", nil, 422); !strings.Contains(code, "invalid_date") {
		t.Fatalf("bad date answered %q", code)
	}
}

func TestMailboxAccessAuditTokenSMTP(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	w.admin.must("PUT", "/organizations/"+w.orgID+"/mailbox-audit", map[string]any{"enabled": true, "logReads": true, "retentionDays": 30}, nil, 200)
	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider idOnly
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret, "autoProvision": true,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)
	f.subject, f.email = "halo-"+randomSuffix(), "dave@"+w.domain
	oidcLogin(t, f, slug)
	w.admin.must("POST", "/mailboxes/"+w.aliceMailbox+"/delegates", map[string]any{"email": f.email, "sendAs": true}, nil, 200)

	c, err := smtp.DialStartTLS(env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), mailTLS(t))
	if err != nil {
		t.Fatalf("smtp dial: %v", err)
	}
	defer c.Close()
	if err := c.Auth(sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{Username: alice, Token: f.mint(f.subject, f.clientID)})); err != nil {
		t.Fatalf("delegate token SMTP login as the owner: %v", err)
	}
	subject := "token delegate " + randomSuffix()
	if err := c.SendMail(alice, []string{alice}, strings.NewReader(rfc822(alice, alice, subject, "x"))); err != nil {
		t.Fatalf("delegate token send as the owner: %v", err)
	}
	waitFor(t, "the token delegate's SMTP send to be recorded", func() bool {
		var l accessLog
		w.admin.must("GET", "/mailboxes/"+w.aliceMailbox+"/access-log", nil, &l, 200)
		return l.has(f.email, "message.send_as", "smtp", func(s, _ string) bool { return s == subject })
	})
}

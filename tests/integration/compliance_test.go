//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type caseSearch struct {
	Results []struct {
		MailboxID string `json:"mailboxId"`
		Subject   string `json:"subject"`
		Folder    string `json:"folder"`
	} `json:"results"`
	Skipped []string `json:"skipped"`
	Failed  []struct {
		Mailbox string `json:"mailbox"`
	} `json:"failed"`
}

func readZip(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("export is not a zip (%q): %v", body[:min(len(body), 200)], err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(data)
	}
	return files
}

func TestComplianceSearchHoldExport(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	alice := "alice@" + w.domain
	if code := errorCode(t, w.admin, "GET", "/organizations/"+w.orgID+"/compliance/cases", nil, 403); code != "forbidden" {
		t.Fatalf("case list without the compliance role: %s", code)
	}
	if code := errorCode(t, w.alice, "POST", "/organizations/"+w.orgID+"/members", map[string]any{"email": alice, "role": "member", "compliance": true}, 403); code != "forbidden" {
		t.Fatalf("a member granted the compliance role: %s", code)
	}
	ownerSetsAdminRole(t, w, map[string]any{"role": "owner", "compliance": true})

	subject := "Quarterly ledger " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "auditor@external.example", []string{alice}, rfc822("auditor@external.example", alice, subject, "confidential figures"), false); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	waitFor(t, "delivery to alice", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", subject) != 0 })

	var c struct {
		ID string `json:"id"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/compliance/cases", map[string]any{
		"name": "Ledger review", "description": "Integration", "criteria": map[string]any{"scope": "mailboxes", "mailboxIds": []string{w.aliceMailbox, w.bobMailbox}, "body": "confidential", "since": time.Now().AddDate(0, 0, -1).Format(time.DateOnly)},
	}, &c, 201)
	var found caseSearch
	w.admin.must("POST", "/compliance/cases/"+c.ID+"/search", nil, &found, 200)
	if len(found.Results) != 1 || found.Results[0].Subject != subject || found.Results[0].MailboxID != w.aliceMailbox || len(found.Failed) != 0 {
		t.Fatalf("search: %+v", found)
	}

	files := readZip(t, w.admin.raw("GET", "/compliance/cases/"+c.ID+"/export?format=eml"))
	manifest, err := csv.NewReader(strings.NewReader(files["manifest.csv"])).ReadAll()
	if err != nil || len(manifest) != 2 || manifest[1][0] != alice || manifest[1][8] != subject {
		t.Fatalf("eml manifest: %v %v", manifest, err)
	}
	if !strings.Contains(files[manifest[1][11]], "confidential figures") {
		t.Fatalf("eml export lacks the message: %v", files)
	}
	files = readZip(t, w.admin.raw("GET", "/compliance/cases/"+c.ID+"/export?format=mbox"))
	if mbox := files[alice+"/INBOX.mbox"]; !strings.HasPrefix(mbox, "From auditor@external.example ") || !strings.Contains(mbox, subject) {
		t.Fatalf("mbox export: %v", files)
	}

	var placed struct {
		Added int `json:"added"`
	}
	w.admin.must("POST", "/compliance/cases/"+c.ID+"/holds", map[string]any{"mailboxIds": []string{w.aliceMailbox}}, &placed, 200)
	if placed.Added != 1 {
		t.Fatalf("holds added: %d", placed.Added)
	}
	var retained struct {
		Active     bool    `json:"active"`
		RetainedAt *string `json:"retainedAt"`
	}
	w.admin.must("DELETE", "/mailboxes/"+w.aliceMailbox, nil, &retained, 200)
	if retained.Active || retained.RetainedAt == nil {
		t.Fatalf("held mailbox was not retained: %+v", retained)
	}
	if code := errorCode(t, w.admin, "DELETE", "/mailboxes/"+w.aliceMailbox, nil, 409); code != "mailbox_retained" {
		t.Fatalf("deleting a retained mailbox: %s", code)
	}
	waitFor(t, "the retained mailbox's sessions to be ended", func() bool {
		found = caseSearch{}
		w.admin.must("POST", "/compliance/cases/"+c.ID+"/search", nil, &found, 200)
		return len(found.Failed) == 0
	})
	if len(found.Results) != 1 || len(found.Skipped) != 0 {
		t.Fatalf("search of a retained mailbox: %+v", found)
	}

	w.admin.must("DELETE", "/compliance/cases/"+c.ID+"/holds/"+w.aliceMailbox, nil, nil, 204)
	found = caseSearch{}
	w.admin.must("POST", "/compliance/cases/"+c.ID+"/search", nil, &found, 200)
	if len(found.Results) != 0 || len(found.Skipped) != 1 || found.Skipped[0] != alice {
		t.Fatalf("search after the hold was released: %+v", found)
	}
	w.admin.must("DELETE", "/mailboxes/"+w.aliceMailbox, nil, nil, 204)

	w.admin.must("PATCH", "/compliance/cases/"+c.ID, map[string]any{"closed": true}, nil, 200)
	if code := errorCode(t, w.admin, "POST", "/compliance/cases/"+c.ID+"/holds", map[string]any{"mailboxIds": []string{w.bobMailbox}}, 409); code != "case_closed" {
		t.Fatalf("hold on a closed case: %s", code)
	}

	var events []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &events, 200)
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Action] = true
	}
	for _, action := range []string{"compliance.case_create", "compliance.search", "compliance.export", "compliance.hold_add", "compliance.hold_release", "mailbox.retain", "compliance.case_close"} {
		if !seen[action] {
			t.Errorf("audit log lacks %s", action)
		}
	}
}

func TestJournaling(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	ownerSetsAdminRole(t, w, map[string]any{"role": "owner", "compliance": true})
	var vault idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "vault", "password": "vault-integration-pw"}, &vault, 201)
	archive := newClient(t)
	archive.login("vault@"+w.domain, "vault-integration-pw")

	if code := errorCode(t, w.admin, "POST", "/organizations/"+w.orgID+"/journal-rules", map[string]string{"match": "@elsewhere.example", "archiveAddress": "vault@" + w.domain}, 422); code != "invalid_match" {
		t.Fatalf("journaling a foreign domain: %s", code)
	}
	var rule idOnly
	w.admin.must("POST", "/organizations/"+w.orgID+"/journal-rules", map[string]string{"match": "@" + w.domain, "archiveAddress": "vault@" + w.domain}, &rule, 201)
	inbound := "Journaled inbound " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "partner@external.example", []string{bob}, rfc822("partner@external.example", bob, inbound, "x"), false); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	waitFor(t, "journal copy of inbound mail", func() bool { return findInAPI(archive, vault.ID, "INBOX", inbound) != 0 })
	if findInAPI(w.bob, w.bobMailbox, "INBOX", inbound) == 0 {
		t.Fatal("the recipient did not get the journaled message")
	}
	w.admin.must("DELETE", "/journal-rules/"+rule.ID, nil, nil, 204)

	w.admin.must("POST", "/organizations/"+w.orgID+"/journal-rules", map[string]string{"match": alice, "archiveAddress": "vault@" + w.domain}, &rule, 201)
	if code := errorCode(t, w.admin, "POST", "/organizations/"+w.orgID+"/journal-rules", map[string]string{"match": alice, "archiveAddress": "vault@" + w.domain}, 409); code != "journal_rule_exists" {
		t.Fatalf("duplicate journal rule: %s", code)
	}
	outbound := "Journaled outbound " + randomSuffix()
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{bob}, rfc822(alice, bob, outbound, "x"), false); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitFor(t, "journal copy of outbound mail", func() bool { return findInAPI(archive, vault.ID, "INBOX", outbound) != 0 })
}

func recoverableSubjects(t *testing.T, c *apiClient, mailbox string) map[string]string {
	t.Helper()
	var out recoverableList
	c.must("GET", "/mailboxes/"+mailbox+"/recoverable", nil, &out, 200)
	subjects := map[string]string{}
	for _, it := range out.Items {
		subjects[it.Subject] = it.Folder
	}
	return subjects
}

func TestScopedHoldKeepsMatchingMail(t *testing.T) {
	w := setupWorld(t)
	db := testDB(t)
	ctx := context.Background()
	alice := "alice@" + w.domain
	suffix := randomSuffix()
	topic := "Atlas " + suffix
	matchOld, otherOld := topic+" old", "lunch "+suffix+" old"
	matchNew, otherNew, draft := topic+" new", "lunch "+suffix+" new", topic+" draft"
	ownerSetsAdminRole(t, w, map[string]any{"role": "owner", "compliance": true})

	retentionDone := func(what string) {
		waitUpTo(t, 2*time.Minute, what, func() bool {
			var done bool
			return db.QueryRow(ctx, `SELECT next_run_at > now() + interval '23 hours' FROM retention_runs WHERE mailbox_id = $1`, w.aliceMailbox).Scan(&done) == nil && done
		})
	}
	retentionDone("the first retention run for alice")

	var c struct {
		ID string `json:"id"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/compliance/cases", map[string]any{"name": "Atlas", "criteria": map[string]any{"scope": "mailboxes", "mailboxIds": []string{w.aliceMailbox}, "subject": topic}}, &c, 201)
	if code := errorCode(t, w.admin, "POST", "/compliance/cases/"+c.ID+"/holds", map[string]any{"mailboxIds": []string{w.aliceMailbox}, "criteria": map[string]any{"since": "2026-02-01", "until": "2026-01-01"}}, 422); code != "invalid_criteria" {
		t.Fatalf("hold with an inverted date range: %s", code)
	}
	w.admin.must("POST", "/compliance/cases/"+c.ID+"/holds", map[string]any{"mailboxIds": []string{w.aliceMailbox}, "criteria": map[string]any{"subject": topic}}, nil, 200)
	waitFor(t, "the hold to end alice's sessions", func() bool {
		var n int
		return db.QueryRow(ctx, `SELECT count(*) FROM mail_kicks WHERE identity = $1`, alice).Scan(&n) == nil && n == 0
	})

	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("imap login: %v", err)
	}
	defer ic.Close()
	old := time.Now().AddDate(0, 0, -100)
	imapAppend(t, ic, "INBOX", rfc822("sender@external.example", alice, matchOld, "body"), old)
	imapAppend(t, ic, "INBOX", rfc822("sender@external.example", alice, otherOld, "body"), old)
	imapAppend(t, ic, "INBOX", rfc822("sender@external.example", alice, matchNew, "body"), time.Now())
	imapAppend(t, ic, "INBOX", rfc822("sender@external.example", alice, otherNew, "body"), time.Now())
	imapAppend(t, ic, "Drafts", rfc822(alice, "someone@external.example", draft, "body"), time.Now())

	w.admin.must("POST", "/organizations/"+w.orgID+"/retention-policies", map[string]any{"action": "delete", "folder": "INBOX", "days": 60, "mailboxId": w.aliceMailbox}, nil, 201)
	retentionDone("retention with the scoped hold")
	if imapFindSubject(t, ic, "INBOX", matchOld) == nil {
		t.Fatal("retention deleted a message inside the hold's scope")
	}
	if imapFindSubject(t, ic, "INBOX", otherOld) != nil {
		t.Fatal("retention kept a message outside the hold's scope")
	}

	for _, folder := range []string{"INBOX", "Drafts"} {
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			t.Fatal(err)
		}
		if err := ic.Store(imap.SeqSet{{Start: 1}}, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
			t.Fatalf("flag %s: %v", folder, err)
		}
		if err := ic.Expunge().Close(); err != nil {
			t.Fatalf("expunge %s: %v", folder, err)
		}
	}
	got := recoverableSubjects(t, w.alice, w.aliceMailbox)
	for _, s := range []string{matchOld, otherOld, matchNew, otherNew, draft} {
		if _, ok := got[s]; !ok {
			t.Errorf("%q is not recoverable after deletion under a hold: %v", s, got)
		}
	}

	if _, err := ic.Select(".EXPUNGED/INBOX", nil).Wait(); err != nil {
		t.Fatalf("select the recoverable store: %v", err)
	}
	ic.Store(imap.SeqSet{{Start: 1}}, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close()
	ic.Expunge().Close()
	if after := recoverableSubjects(t, w.alice, w.aliceMailbox); len(after) != len(got) {
		t.Fatalf("a held mailbox's owner expunged recoverable mail: before %v, after %v", got, after)
	}

	var found caseSearch
	w.admin.must("POST", "/compliance/cases/"+c.ID+"/search", nil, &found, 200)
	folders := map[string]string{}
	for _, r := range found.Results {
		folders[r.Subject] = r.Folder
	}
	if folders[matchOld] != ".EXPUNGED/INBOX" || folders[matchNew] != ".EXPUNGED/INBOX" || folders[draft] != ".EXPUNGED/Drafts" {
		t.Fatalf("case search over deleted mail: %+v", found)
	}

	purge := func() {
		t.Helper()
		purgeRecoverableNow(t, db, w.aliceMailbox, alice)
	}
	purge()
	got = recoverableSubjects(t, w.alice, w.aliceMailbox)
	for s, kept := range map[string]bool{matchOld: true, matchNew: true, draft: true, otherOld: false, otherNew: false} {
		if _, ok := got[s]; ok != kept {
			t.Errorf("after the purge %q recoverable = %v, want %v", s, ok, kept)
		}
	}

	var restored struct {
		Restored int `json:"restored"`
	}
	var items recoverableList
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/recoverable", nil, &items, 200)
	for _, it := range items.Items {
		if it.Subject == matchNew {
			w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/recoverable/restore", map[string]any{"items": []map[string]any{{"folder": it.Folder, "uid": it.UID}}}, &restored, 200)
		}
	}
	if restored.Restored != 1 || findInAPI(w.alice, w.aliceMailbox, "INBOX", matchNew) == 0 {
		t.Fatalf("restoring under a hold: %+v", restored)
	}

	w.admin.must("DELETE", "/compliance/cases/"+c.ID+"/holds/"+w.aliceMailbox, nil, nil, 204)
	purge()
	if left := recoverableSubjects(t, w.alice, w.aliceMailbox); len(left) != 0 {
		t.Fatalf("recoverable mail left after the hold was released and the store purged: %v", left)
	}
}

func purgeRecoverableNow(t *testing.T, db *pgx.Conn, mailboxID, address string) {
	t.Helper()
	time.Sleep(2 * time.Second)
	ctx := context.Background()
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(criteria), '[]') FROM mailbox_holds WHERE mailbox_id = $1`, mailboxID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var scopes []*struct {
		Subject    string `json:"subject"`
		Attachment string `json:"attachment"`
	}
	if err := json.Unmarshal(raw, &scopes); err != nil {
		t.Fatal(err)
	}
	var queries []mailpkg.SearchQuery
	for _, s := range scopes {
		if s == nil {
			queries = append(queries, mailpkg.SearchQuery{})
			continue
		}
		queries = append(queries, mailpkg.SearchQuery{Subject: s.Subject, Attachment: s.Attachment})
	}
	mc, err := mailpkg.NewClient(env(t, "AETHER_TEST_IMAP", "dovecot:143"), "", "", "", "", env(t, "AETHER_TEST_MAIL_HOSTNAME", "mail.aether.test"), env(t, "AETHER_TEST_CA_FILE", "/tls/fullchain.pem"),
		env(t, "AETHER_TEST_DOVECOT_MASTER_USER", "aether-webmail"), env(t, "AETHER_TEST_DOVECOT_MASTER_PASSWORD", ""), env(t, "AETHER_TEST_DOVEADM", "http://dovecot:8089"), env(t, "AETHER_TEST_DOVECOT_API_KEY", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := mc.PurgeRecoverable(ctx, address, 0, 0, mailpkg.NewHoldScope(queries)); err != nil {
		t.Fatalf("purge: %v", err)
	}
}

func withAttachment(from, to, subject, filename string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=part\r\n\r\n--part\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--part\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"%s\"\r\nContent-Transfer-Encoding: base64\r\n\r\neA==\r\n--part--\r\n",
		from, to, subject, time.Now().Format(time.RFC1123Z), filename)
}

func TestAttachmentScopedHolds(t *testing.T) {
	w := setupWorld(t)
	db := testDB(t)
	ctx := context.Background()
	alice := "alice@" + w.domain
	suffix := randomSuffix()
	topic := "Atlas " + suffix
	contract, photo := "contract only "+suffix, "photo only "+suffix
	topicInvoice, topicPhoto, otherInvoice, plain := topic+" invoice", topic+" photo", "other invoice "+suffix, "plain "+suffix
	ownerSetsAdminRole(t, w, map[string]any{"role": "owner", "compliance": true})
	retentionDone := func(what string) {
		waitUpTo(t, 2*time.Minute, what, func() bool {
			var done bool
			return db.QueryRow(ctx, `SELECT next_run_at > now() + interval '23 hours' FROM retention_runs WHERE mailbox_id = $1`, w.aliceMailbox).Scan(&done) == nil && done
		})
	}
	retentionDone("the first retention run for alice")

	holds := map[string]map[string]any{"Contracts": {"attachment": "CONTRACT"}, "Atlas invoices": {"subject": topic, "attachment": "invoice"}}
	caseIDs := map[string]string{}
	for name, criteria := range holds {
		var c idOnly
		w.admin.must("POST", "/organizations/"+w.orgID+"/compliance/cases", map[string]any{"name": name, "criteria": map[string]any{"scope": "organization"}}, &c, 201)
		w.admin.must("POST", "/compliance/cases/"+c.ID+"/holds", map[string]any{"mailboxIds": []string{w.aliceMailbox}, "criteria": criteria}, nil, 200)
		caseIDs[name] = c.ID
	}
	waitFor(t, "the holds to end alice's sessions", func() bool {
		var n int
		return db.QueryRow(ctx, `SELECT count(*) FROM mail_kicks WHERE identity = $1`, alice).Scan(&n) == nil && n == 0
	})

	ic, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("imap login: %v", err)
	}
	defer ic.Close()
	old := time.Now().AddDate(0, 0, -100)
	from := "sender@external.example"
	imapAppend(t, ic, "INBOX", withAttachment(from, alice, contract, "Signed-Contract-2026.pdf"), old)
	imapAppend(t, ic, "INBOX", withAttachment(from, alice, photo, "photo.jpg"), old)
	imapAppend(t, ic, "INBOX", withAttachment(from, alice, topicInvoice, "invoice-17.pdf"), old)
	imapAppend(t, ic, "INBOX", withAttachment(from, alice, topicPhoto, "photo.jpg"), old)
	imapAppend(t, ic, "INBOX", withAttachment(from, alice, otherInvoice, "invoice-18.pdf"), old)
	imapAppend(t, ic, "INBOX", rfc822(from, alice, plain, "body"), old)
	kept := map[string]bool{contract: true, photo: false, topicInvoice: true, topicPhoto: false, otherInvoice: false, plain: false}

	imapProxy := dovecotProxy(t, "imap")
	imapProxy.cut()
	w.admin.must("POST", "/organizations/"+w.orgID+"/retention-policies", map[string]any{"action": "delete", "folder": "INBOX", "days": 60, "mailboxId": w.aliceMailbox}, nil, 201)
	waitUpTo(t, 2*time.Minute, "retention to fail while the API cannot check attachments", func() bool {
		var failed bool
		return db.QueryRow(ctx, `SELECT lease_until IS NULL AND next_run_at BETWEEN now() + interval '50 minutes' AND now() + interval '2 hours' FROM retention_runs WHERE mailbox_id = $1`, w.aliceMailbox).Scan(&failed) == nil && failed
	})
	for subject := range kept {
		if imapFindSubject(t, ic, "INBOX", subject) == nil {
			t.Errorf("retention expunged %q although the attachment check failed", subject)
		}
	}
	imapProxy.restore()
	if _, err := db.Exec(ctx, `UPDATE retention_runs SET next_run_at = now() WHERE mailbox_id = $1`, w.aliceMailbox); err != nil {
		t.Fatal(err)
	}
	retentionDone("retention with attachment-scoped holds")
	for subject, want := range kept {
		if got := imapFindSubject(t, ic, "INBOX", subject) != nil; got != want {
			t.Errorf("after retention %q in INBOX = %v, want %v", subject, got, want)
		}
	}

	if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := ic.Store(imap.SeqSet{{Start: 1}}, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := ic.Expunge().Close(); err != nil {
		t.Fatal(err)
	}
	if got := recoverableSubjects(t, w.alice, w.aliceMailbox); len(got) != len(kept) {
		t.Fatalf("recoverable before the purge: %v", got)
	}

	purgeRecoverableNow(t, db, w.aliceMailbox, alice)
	got := recoverableSubjects(t, w.alice, w.aliceMailbox)
	for subject, want := range kept {
		if _, ok := got[subject]; ok != want {
			t.Errorf("after the purge %q recoverable = %v, want %v", subject, ok, want)
		}
	}

	for _, id := range caseIDs {
		w.admin.must("DELETE", "/compliance/cases/"+id+"/holds/"+w.aliceMailbox, nil, nil, 204)
	}
	purgeRecoverableNow(t, db, w.aliceMailbox, alice)
	if left := recoverableSubjects(t, w.alice, w.aliceMailbox); len(left) != 0 {
		t.Fatalf("recoverable mail left after the holds were released: %v", left)
	}
}

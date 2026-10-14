//go:build integration

package integration

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

type world struct {
	admin              *apiClient
	orgID, domain      string
	domainID           string
	aliceMailbox       string
	bobMailbox         string
	alice, bob         *apiClient
	aliceAppPassword   string
	aliceAppPasswordID string
}

func setupWorld(t *testing.T) *world {
	w := &world{admin: adminClient(t)}
	suffix := randomSuffix()
	var org idOnly
	w.admin.must("POST", "/organizations", map[string]string{"name": "Integration " + suffix, "slug": "it-" + suffix}, &org, 201)
	w.orgID = org.ID
	w.domain = "it" + suffix + ".test"
	var dom struct {
		ID       string `json:"id"`
		Verified bool   `json:"verified"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/domains", map[string]string{"name": w.domain}, &dom, 201)
	if !dom.Verified {
		t.Fatal("domain added by a platform administrator should be verified")
	}
	w.domainID = dom.ID
	for _, m := range []struct {
		local string
		dst   *string
	}{{"alice", &w.aliceMailbox}, {"bob", &w.bobMailbox}} {
		var mb idOnly
		w.admin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{
			"domainId": w.domainID, "localPart": m.local, "displayName": strings.ToUpper(m.local[:1]) + m.local[1:], "password": m.local + "-integration-pw",
		}, &mb, 201)
		*m.dst = mb.ID
	}
	w.alice = newClient(t)
	w.alice.login("alice@"+w.domain, "alice-integration-pw")
	w.bob = newClient(t)
	w.bob.login("bob@"+w.domain, "bob-integration-pw")
	var ap struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	w.alice.must("POST", "/me/app-passwords", map[string]string{"name": "integration", "mailboxId": w.aliceMailbox}, &ap, 201)
	w.aliceAppPassword, w.aliceAppPasswordID = ap.Password, ap.ID
	return w
}

type messagePage struct {
	Messages []struct {
		UID     uint32 `json:"uid"`
		Subject string `json:"subject"`
		Seen    bool   `json:"seen"`
		From    []struct {
			Address string `json:"address"`
		} `json:"from"`
	} `json:"messages"`
}

func findInAPI(c *apiClient, mailbox, folder, subject string) uint32 {
	var page messagePage
	c.must("GET", "/mailboxes/"+mailbox+"/messages?folder="+folder, nil, &page, 200)
	for _, m := range page.Messages {
		if m.Subject == subject {
			return m.UID
		}
	}
	return 0
}

func TestMailMilestone(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain

	t.Run("submission with app password delivers and signs DKIM", func(t *testing.T) {
		subject := "submission " + randomSuffix()
		if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{bob}, rfc822(alice, bob, subject, "sent over 587"), false); err != nil {
			t.Fatalf("submission: %v", err)
		}
		waitFor(t, "bob to receive the message in the API", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 })
		bobPass := createAppPassword(t, w.bob, w.bobMailbox)
		ic, err := imapLogin(t, bob, bobPass)
		if err != nil {
			t.Fatalf("bob imap login: %v", err)
		}
		defer ic.Close()
		raw := imapFindSubject(t, ic, "INBOX", subject)
		if !bytes.Contains(raw, []byte("DKIM-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed; d="+w.domain)) {
			t.Fatalf("message is not DKIM signed for %s:\n%s", w.domain, raw)
		}
		if bytes.Contains(raw, []byte("with ESMTPSA")) {
			t.Fatalf("authenticated submission hop leaked into headers:\n%s", raw)
		}
	})

	t.Run("inbound SMTP from an external sender reaches IMAP", func(t *testing.T) {
		subject := "inbound " + randomSuffix()
		if err := submit(t, smtpAddr, "", "", "sender@external.example", []string{alice}, rfc822("sender@external.example", alice, subject, "hello from outside"), false); err != nil {
			t.Fatalf("port 25 delivery: %v", err)
		}
		ic, err := imapLogin(t, alice, w.aliceAppPassword)
		if err != nil {
			t.Fatalf("alice imap login: %v", err)
		}
		defer ic.Close()
		waitFor(t, "alice to see the inbound message over IMAP", func() bool { return imapFindSubject(t, ic, "INBOX", subject) != nil })
	})

	t.Run("webmail send, read, reply, and flags", func(t *testing.T) {
		subject := "webmail " + randomSuffix()
		w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{
			"to": []map[string]string{{"address": alice, "name": "Alice"}}, "subject": subject,
			"text": "Plain text body", "html": `<p>Rich <b>body</b></p><script>alert(1)</script><img src="https://tracker.example/p.gif">`,
		}, nil, 202)
		var uid uint32
		waitFor(t, "alice to receive the webmail message", func() bool { uid = findInAPI(w.alice, w.aliceMailbox, "INBOX", subject); return uid != 0 })
		if findInAPI(w.bob, w.bobMailbox, "Sent", subject) == 0 {
			t.Fatal("sent message was not saved to Sent")
		}
		var msg struct {
			MessageID string `json:"messageId"`
			Text      string `json:"text"`
			HTML      string `json:"html"`
			Seen      bool   `json:"seen"`
		}
		path := "/mailboxes/" + w.aliceMailbox + "/messages/" + strconv.FormatUint(uint64(uid), 10)
		w.alice.must("GET", path, nil, &msg, 200)
		if !strings.Contains(msg.Text, "Plain text body") || !strings.Contains(msg.HTML, "<b>body</b>") {
			t.Fatalf("unexpected body: text=%q html=%q", msg.Text, msg.HTML)
		}
		if strings.Contains(msg.HTML, "<script") || strings.Contains(msg.HTML, "tracker.example") {
			t.Fatalf("html was not sanitized: %s", msg.HTML)
		}
		w.alice.must("PATCH", path, map[string]bool{"seen": true, "flagged": true}, nil, 204)
		w.alice.must("GET", path, nil, &msg, 200)
		if !msg.Seen {
			t.Fatal("message not marked seen")
		}
		reply := "Re: " + subject
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{
			"to": []map[string]string{{"address": bob}}, "subject": reply, "text": "Got it", "inReplyTo": msg.MessageID,
		}, nil, 202)
		var replyUID uint32
		waitFor(t, "bob to receive the reply", func() bool { replyUID = findInAPI(w.bob, w.bobMailbox, "INBOX", reply); return replyUID != 0 })
		var replyMsg struct {
			Text string `json:"text"`
		}
		w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages/"+strconv.FormatUint(uint64(replyUID), 10), nil, &replyMsg, 200)
		if !strings.Contains(replyMsg.Text, "Got it") {
			t.Fatalf("plain-text reply arrived without its body: %q", replyMsg.Text)
		}
		w.alice.must("DELETE", path, nil, nil, 204)
		if findInAPI(w.alice, w.aliceMailbox, "Trash", subject) == 0 {
			t.Fatal("deleted message did not move to Trash")
		}
	})

	t.Run("attachments are sent, downloaded, and forwarded", func(t *testing.T) {
		subject := "attachment " + randomSuffix()
		payload := []byte("binary\x00payload " + randomSuffix())
		w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{
			"to": []map[string]string{{"address": alice}}, "subject": subject, "text": "see file",
			"attachments": []map[string]any{{"filename": "report.bin", "contentType": "application/octet-stream", "data": payload}},
		}, nil, 202)
		var uid uint32
		waitFor(t, "alice to receive the attachment", func() bool { uid = findInAPI(w.alice, w.aliceMailbox, "INBOX", subject); return uid != 0 })
		var msg struct {
			Attachments []struct {
				Part     string `json:"part"`
				Filename string `json:"filename"`
			} `json:"attachments"`
		}
		base := "/mailboxes/" + w.aliceMailbox + "/messages/" + strconv.FormatUint(uint64(uid), 10)
		w.alice.must("GET", base, nil, &msg, 200)
		if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != "report.bin" {
			t.Fatalf("attachment metadata: %+v", msg.Attachments)
		}
		if got := w.alice.raw("GET", base+"/attachments/"+msg.Attachments[0].Part); !bytes.Equal(got, payload) {
			t.Fatalf("downloaded %q, want %q", got, payload)
		}
		fwd := "Fwd: " + subject
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{
			"to": []map[string]string{{"address": bob}}, "subject": fwd, "text": "forwarding",
			"forwardFrom": map[string]any{"folder": "INBOX", "uid": uid},
		}, nil, 202)
		var fuid uint32
		waitFor(t, "bob to receive the forward", func() bool { fuid = findInAPI(w.bob, w.bobMailbox, "INBOX", fwd); return fuid != 0 })
		w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages/"+strconv.FormatUint(uint64(fuid), 10), nil, &msg, 200)
		if len(msg.Attachments) != 1 || msg.Attachments[0].Filename != "report.bin" {
			t.Fatalf("forward lost the attachment: %+v", msg.Attachments)
		}
	})

	t.Run("drafts autosave, keep attachments, and disappear on send", func(t *testing.T) {
		subject := "draft " + randomSuffix()
		type saved struct {
			UID         uint32 `json:"uid"`
			Folder      string `json:"folder"`
			Attachments []struct {
				Part     string `json:"part"`
				Filename string `json:"filename"`
			} `json:"attachments"`
		}
		var first, second, third saved
		w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/drafts", map[string]any{"subject": subject, "text": "first pass"}, &first, 200)
		w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/drafts", map[string]any{
			"draftUid": first.UID, "subject": subject, "text": "second pass",
			"to": []map[string]string{{"address": bob}}, "bcc": []map[string]string{{"address": "audit@" + w.domain}},
			"attachments": []map[string]any{{"filename": "keep.txt", "data": []byte("keep me")}, {"filename": "drop.txt", "data": []byte("drop me")}},
		}, &second, 200)
		if second.UID == first.UID || len(second.Attachments) != 2 {
			t.Fatalf("second save: %+v", second)
		}
		var keep string
		for _, a := range second.Attachments {
			if a.Filename == "keep.txt" {
				keep = a.Part
			}
		}
		w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/drafts", map[string]any{
			"draftUid": second.UID, "subject": subject, "text": "third pass", "to": []map[string]string{{"address": bob}},
			"bcc":         []map[string]string{{"address": "audit@" + w.domain}},
			"forwardFrom": map[string]any{"folder": second.Folder, "uid": second.UID, "parts": []string{keep}},
		}, &third, 200)
		if len(third.Attachments) != 1 || third.Attachments[0].Filename != "keep.txt" {
			t.Fatalf("third save kept %+v, want only keep.txt", third.Attachments)
		}
		var page messagePage
		w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/messages?folder="+third.Folder, nil, &page, 200)
		count := 0
		for _, m := range page.Messages {
			if m.Subject == subject {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%d copies of the draft in %s, want 1", count, third.Folder)
		}
		var draft struct {
			Bcc []struct {
				Address string `json:"address"`
			} `json:"bcc"`
			Text string `json:"text"`
		}
		w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/messages/"+strconv.FormatUint(uint64(third.UID), 10)+"?folder="+third.Folder, nil, &draft, 200)
		if len(draft.Bcc) != 1 || !strings.Contains(draft.Text, "third pass") {
			t.Fatalf("reopened draft lost data: %+v", draft)
		}
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{
			"draftUid": third.UID, "subject": subject, "text": "final", "to": []map[string]string{{"address": bob}},
			"forwardFrom": map[string]any{"folder": third.Folder, "uid": third.UID},
		}, nil, 202)
		var uid uint32
		waitFor(t, "bob to receive the sent draft", func() bool { uid = findInAPI(w.bob, w.bobMailbox, "INBOX", subject); return uid != 0 })
		var got struct {
			Attachments []struct {
				Filename string `json:"filename"`
			} `json:"attachments"`
		}
		w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages/"+strconv.FormatUint(uint64(uid), 10), nil, &got, 200)
		if len(got.Attachments) != 1 || got.Attachments[0].Filename != "keep.txt" {
			t.Fatalf("sent draft attachments: %+v", got.Attachments)
		}
		if findInAPI(w.alice, w.aliceMailbox, third.Folder, subject) != 0 {
			t.Fatal("draft was not removed after sending")
		}
	})

	t.Run("conversations are threaded", func(t *testing.T) {
		subject := "topic " + randomSuffix()
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "one"}, nil, 202)
		var first uint32
		waitFor(t, "first message", func() bool { first = findInAPI(w.bob, w.bobMailbox, "INBOX", subject); return first != 0 })
		var original struct {
			MessageID string `json:"messageId"`
		}
		w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages/"+strconv.FormatUint(uint64(first), 10), nil, &original, 200)
		w.bob.must("PATCH", "/mailboxes/"+w.bobMailbox+"/messages/"+strconv.FormatUint(uint64(first), 10), map[string]bool{"seen": true}, nil, 204)
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{
			"to": []map[string]string{{"address": bob}}, "subject": "Re: " + subject, "text": "two", "inReplyTo": original.MessageID, "references": []string{original.MessageID},
		}, nil, 202)
		waitFor(t, "follow-up", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", "Re: "+subject) != 0 })
		var threaded struct {
			Messages []struct {
				Subject    string   `json:"subject"`
				Seen       bool     `json:"seen"`
				ThreadUIDs []uint32 `json:"threadUids"`
			} `json:"messages"`
		}
		w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages?folder=INBOX&threads=true", nil, &threaded, 200)
		rows := 0
		for _, m := range threaded.Messages {
			if strings.HasSuffix(m.Subject, subject) {
				rows++
				if len(m.ThreadUIDs) != 2 || m.ThreadUIDs[1] != first || m.Seen {
					t.Fatalf("thread row %+v: want 2 uids ending with %d and unread", m, first)
				}
			}
		}
		if rows != 1 {
			t.Fatalf("%d rows for the conversation, want 1", rows)
		}
	})

	t.Run("mail rules file and mark incoming mail", func(t *testing.T) {
		tag := "invoice-" + randomSuffix()
		if got := w.bob.do("PUT", "/mailboxes/"+w.bobMailbox+"/rules", []map[string]any{{
			"name": "bad folder", "enabled": true, "match": "all", "conditions": []map[string]string{{"field": "subject", "contains": tag}}, "actions": map[string]any{"moveTo": "Nope"},
		}}, nil); got != 422 {
			t.Fatalf("rule with a missing folder returned %d", got)
		}
		w.bob.must("PUT", "/mailboxes/"+w.bobMailbox+"/rules", []map[string]any{{
			"name": "Invoices", "enabled": true, "match": "any",
			"conditions": []map[string]string{{"field": "subject", "contains": tag}, {"field": "from", "contains": "never-matches@"}},
			"actions":    map[string]any{"moveTo": "Archive", "markRead": true, "stop": true},
		}}, nil, 200)
		var stored []map[string]any
		w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/rules", nil, &stored, 200)
		if len(stored) != 1 {
			t.Fatalf("stored rules: %v", stored)
		}
		subject := "Your " + tag
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": subject, "text": "amount due"}, nil, 202)
		var page messagePage
		waitFor(t, "the rule to file the message into Archive", func() bool {
			w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages?folder=Archive", nil, &page, 200)
			for _, m := range page.Messages {
				if m.Subject == subject {
					return m.Seen
				}
			}
			return false
		})
		if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) != 0 {
			t.Fatal("rule-matched message also landed in the inbox")
		}
		w.bob.must("PUT", "/mailboxes/"+w.bobMailbox+"/rules", []map[string]any{}, nil, 200)
	})

	t.Run("forwarding and automatic replies go out through the sieve relay", func(t *testing.T) {
		tag := "forward-" + randomSuffix()
		w.bob.must("PUT", "/mailboxes/"+w.bobMailbox+"/rules", []map[string]any{{
			"name": "Forward", "enabled": true, "match": "all", "conditions": []map[string]string{{"field": "subject", "contains": tag}},
			"actions": map[string]any{"forwardTo": []string{alice}, "keepCopy": true},
		}}, nil, 200)
		defer w.bob.must("PUT", "/mailboxes/"+w.bobMailbox+"/rules", []map[string]any{}, nil, 200)
		w.alice.must("POST", "/mailboxes/"+w.aliceMailbox+"/send", map[string]any{"to": []map[string]string{{"address": bob}}, "subject": "Please read " + tag, "text": "forward me"}, nil, 202)
		waitFor(t, "bob to keep a copy", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", "Please read "+tag) != 0 })
		waitFor(t, "the forwarded copy to reach alice", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", "Please read "+tag) != 0 })

		if got := w.alice.do("PUT", "/mailboxes/"+w.aliceMailbox+"/auto-reply", map[string]any{"enabled": true, "message": "x", "start": "2026-12-01", "end": "2026-11-01"}, nil); got != 422 {
			t.Fatalf("inverted date range returned %d", got)
		}
		w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/auto-reply", map[string]any{"enabled": true, "subject": "Out of office " + tag, "message": "Back next week."}, nil, 200)
		defer w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/auto-reply", map[string]any{"enabled": false, "subject": "", "message": ""}, nil, 200)
		var reply struct {
			Enabled bool   `json:"enabled"`
			Subject string `json:"subject"`
		}
		w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/auto-reply", nil, &reply, 200)
		if !reply.Enabled || reply.Subject != "Out of office "+tag {
			t.Fatalf("stored auto-reply: %+v", reply)
		}
		w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", map[string]any{"to": []map[string]string{{"address": alice}}, "subject": "Question " + tag, "text": "are you there?"}, nil, 202)
		waitFor(t, "bob to receive the automatic reply", func() bool { return findInAPI(w.bob, w.bobMailbox, "INBOX", "Out of office "+tag) != 0 })
	})

	t.Run("spam is filed into Junk", func(t *testing.T) {
		subject := "gtube " + randomSuffix()
		body := "XJS*C4JDBQADN1.NSBN3*2IDNEN*GTUBE-STANDARD-ANTI-UBE-TEST-EMAIL*C.34X"
		err := submit(t, smtpAddr, "", "", "spammer@external.example", []string{alice}, rfc822("spammer@external.example", alice, subject, body), false)
		if err == nil {
			waitFor(t, "spam to land in Junk", func() bool { return findInAPI(w.alice, w.aliceMailbox, "Junk", subject) != 0 })
		} else if !strings.Contains(err.Error(), "5") {
			t.Fatalf("unexpected error for GTUBE: %v", err)
		}
		if findInAPI(w.alice, w.aliceMailbox, "INBOX", subject) != 0 {
			t.Fatal("GTUBE message reached the inbox")
		}
	})

	t.Run("relay and spoofing are refused", func(t *testing.T) {
		if err := submit(t, smtpAddr, "", "", "attacker@external.example", []string{"victim@elsewhere.example"}, rfc822("attacker@external.example", "victim@elsewhere.example", "relay", "x"), false); err == nil {
			t.Fatal("unauthenticated relay to an external domain was accepted")
		}
		if err := submit(t, submissionAddr, alice, w.aliceAppPassword, bob, []string{bob}, rfc822(bob, bob, "spoof", "x"), false); err == nil {
			t.Fatal("alice was allowed to send as bob")
		}
		if err := submit(t, submissionAddr, alice, w.aliceAppPassword, "ceo@external.example", []string{bob}, rfc822("ceo@external.example", bob, "spoof", "x"), false); err == nil {
			t.Fatal("alice was allowed to send as an external address")
		}
		if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{bob}, rfc822("CEO <ceo@"+w.domain+">", bob, "header spoof", "x"), false); err == nil || !strings.Contains(err.Error(), "From header") {
			t.Fatalf("forged header From was not rejected: %v", err)
		}
	})

	t.Run("protocol credentials are scoped and revocable", func(t *testing.T) {
		if _, err := imapLogin(t, alice, "alice-integration-pw"); err == nil {
			t.Fatal("web password was accepted for IMAP")
		}
		if _, err := imapLogin(t, bob, w.aliceAppPassword); err == nil {
			t.Fatal("alice's app password opened bob's mailbox")
		}
		w.alice.must("DELETE", "/me/app-passwords/"+w.aliceAppPasswordID, nil, nil, 204)
		if _, err := imapLogin(t, alice, w.aliceAppPassword); err == nil {
			t.Fatal("revoked app password still works")
		}
		if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{bob}, rfc822(alice, bob, "revoked", "x"), false); err == nil {
			t.Fatal("revoked app password still submits mail")
		}
	})
}

func createAppPassword(t *testing.T, c *apiClient, mailbox string) string {
	var ap struct {
		Password string `json:"password"`
	}
	c.must("POST", "/me/app-passwords", map[string]string{"name": "integration", "mailboxId": mailbox}, &ap, 201)
	return ap.Password
}

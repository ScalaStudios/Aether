//go:build integration

package integration

import (
	"bytes"
	"strings"
	"testing"
)

func TestMailFlowRules(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": alice, "role": "member"}, nil, 204)
	rules := "/organizations/" + w.orgID + "/mail-flow/rules"

	var footer, blocked, watch, approval idOnly
	w.admin.must("POST", rules, map[string]any{
		"name": "Internal footer", "mode": "enforce",
		"conditions": []map[string]any{{"field": "direction", "values": []string{"internal"}}},
		"actions":    map[string]any{"disclaimer": map[string]any{"text": "Internal use only", "fallback": "wrap"}, "addHeaders": []map[string]string{{"name": "X-Department", "value": "Integration"}}},
	}, &footer, 201)
	w.admin.must("POST", rules, map[string]any{
		"name": "Block zip", "mode": "enforce",
		"conditions": []map[string]any{{"field": "subject", "values": []string{"forbidden-zip"}}},
		"actions":    map[string]any{"reject": "Zip files are not accepted here"},
	}, &blocked, 201)
	w.admin.must("POST", rules, map[string]any{
		"name": "Watch partners", "mode": "test",
		"conditions": []map[string]any{{"field": "sender", "scope": "domain", "values": []string{"partner.example"}}},
		"actions":    map[string]any{"bcc": []string{alice}},
	}, &watch, 201)
	w.admin.must("POST", rules, map[string]any{
		"name": "Approve payments", "mode": "enforce",
		"conditions": []map[string]any{{"field": "subject", "values": []string{"payment request"}}},
		"actions":    map[string]any{"approvers": []string{alice}},
	}, &approval, 201)
	if code := w.admin.do("POST", rules, map[string]any{"name": "bad", "mode": "enforce", "actions": map[string]any{"approvers": []string{"nobody@" + w.domain}}}, nil); code != 422 {
		t.Fatalf("approver outside the organization: %d, want 422", code)
	}
	w.admin.must("PUT", "/organizations/"+w.orgID+"/mail-flow/order", map[string]any{"ids": []string{blocked.ID, footer.ID, watch.ID, approval.ID}}, nil, 204)

	bobPass := createAppPassword(t, w.bob, w.bobMailbox)
	ic, err := imapLogin(t, bob, bobPass)
	if err != nil {
		t.Fatalf("bob imap login: %v", err)
	}
	defer ic.Close()

	subject := "flow " + randomSuffix()
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{bob}, rfc822(alice, bob, subject, "hello bob"), false); err != nil {
		t.Fatalf("internal submission: %v", err)
	}
	var raw []byte
	waitFor(t, "the internal message with its footer", func() bool { raw = imapFindSubject(t, ic, "INBOX", subject); return raw != nil })
	if !bytes.Contains(raw, []byte("Internal use only")) || !bytes.Contains(raw, []byte("X-Department: Integration")) {
		t.Fatalf("footer or header missing:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("DKIM-Signature:")) || bytes.Contains(raw, []byte("X-Aether-Flow")) {
		t.Fatalf("signature missing or internal header leaked:\n%s", raw)
	}

	err = submit(t, smtpAddr, "", "", "someone@partner.example", []string{bob}, rfc822("someone@partner.example", bob, "forbidden-zip", "x"), false)
	if err == nil || !strings.Contains(err.Error(), "Zip files are not accepted here") {
		t.Fatalf("rejected message: %v", err)
	}
	watched := "watched " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "someone@partner.example", []string{bob}, rfc822("someone@partner.example", bob, watched, "x"), false); err != nil {
		t.Fatalf("partner delivery: %v", err)
	}
	waitFor(t, "the watched message", func() bool { return imapFindSubject(t, ic, "INBOX", watched) != nil })
	var matches []struct {
		RuleName string `json:"ruleName"`
		Test     bool   `json:"test"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/mail-flow/matches", nil, &matches, 200)
	found := false
	for _, m := range matches {
		found = found || (m.RuleName == "Watch partners" && m.Test)
	}
	if !found {
		t.Fatalf("test-mode match not logged: %+v", matches)
	}
	if findInAPI(w.alice, w.aliceMailbox, "INBOX", watched) != 0 {
		t.Fatal("a rule in test mode sent a copy")
	}

	held := "payment request " + randomSuffix()
	if err := submit(t, smtpAddr, "", "", "billing@vendor.example", []string{bob}, rfc822("billing@vendor.example", bob, held, "please pay"), false); err != nil {
		t.Fatalf("held delivery: %v", err)
	}
	var waiting []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
		Preview string `json:"preview"`
	}
	waitFor(t, "the message to wait for alice", func() bool {
		w.alice.must("GET", "/me/mail-flow/held", nil, &waiting, 200)
		return len(waiting) == 1 && waiting[0].Subject == held
	})
	if !strings.Contains(waiting[0].Preview, "please pay") {
		t.Fatalf("preview %q", waiting[0].Preview)
	}
	if imapFindSubject(t, ic, "INBOX", held) != nil {
		t.Fatal("held message was delivered before approval")
	}
	if code := w.bob.do("POST", "/mail-flow/held/"+waiting[0].ID+"/approve", nil, nil); code != 404 {
		t.Fatalf("non-approver approved: %d", code)
	}
	w.alice.must("POST", "/mail-flow/held/"+waiting[0].ID+"/approve", nil, nil, 204)
	waitFor(t, "the approved message", func() bool { return imapFindSubject(t, ic, "INBOX", held) != nil })

	var audit []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
	seen := map[string]bool{}
	for _, a := range audit {
		seen[a.Action] = true
	}
	for _, a := range []string{"mail_flow_rule.create", "mail_flow_rule.reorder", "mail_flow.held.approve"} {
		if !seen[a] {
			t.Errorf("audit event %s missing", a)
		}
	}
	w.admin.must("DELETE", rules+"/"+footer.ID, nil, nil, 204)
}

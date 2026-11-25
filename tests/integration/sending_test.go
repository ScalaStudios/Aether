//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestSendingLimitsAndRestriction(t *testing.T) {
	w := setupWorld(t)
	submission := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": alice, "role": "admin"}, nil, 204)
	var ap struct {
		Password string `json:"password"`
	}
	w.bob.must("POST", "/me/app-passwords", map[string]string{"name": "integration", "mailboxId": w.bobMailbox}, &ap, 201)
	send := func(to ...string) error {
		return submit(t, submission, bob, ap.Password, bob, to, rfc822(bob, to[0], "limit "+randomSuffix(), "hello"), false)
	}

	limits := "/organizations/" + w.orgID + "/sending-limits"
	if code := w.admin.do("PUT", limits, map[string]any{"mailboxId": w.bobMailbox, "limits": map[string]int{"spamPercent": 101}}, nil); code != 422 {
		t.Fatalf("out of range limit: %d, want 422", code)
	}
	if code := w.bob.do("PUT", limits, map[string]any{"limits": map[string]int{}}, nil); code != 403 && code != 404 {
		t.Fatalf("member changed sending limits: %d", code)
	}
	w.admin.must("PUT", limits, map[string]any{"limits": map[string]int{"recipientsPerMessage": 2}}, nil, 200)
	w.admin.must("PUT", limits, map[string]any{"mailboxId": w.bobMailbox, "limits": map[string]int{"messagesPerHour": 2}}, nil, 200)
	var settings struct {
		Organization map[string]int            `json:"organization"`
		Mailboxes    map[string]map[string]int `json:"mailboxes"`
	}
	w.admin.must("GET", limits, nil, &settings, 200)
	if settings.Organization["recipientsPerMessage"] != 2 || settings.Mailboxes[w.bobMailbox]["messagesPerHour"] != 2 {
		t.Fatalf("stored limits %+v", settings)
	}

	if err := send(alice, bob, "carol@"+w.domain); err == nil || !strings.Contains(err.Error(), "452") {
		t.Fatalf("third recipient of one message: %v, want 452", err)
	}
	if err := send(alice); err != nil {
		t.Fatalf("second message within the limit: %v", err)
	}
	if err := send(alice); err == nil || !strings.Contains(err.Error(), "exceeded its sending limit") {
		t.Fatalf("message over the hourly limit: %v", err)
	}
	if err := send(alice); err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("restricted sender: %v", err)
	}

	restricted := "/organizations/" + w.orgID + "/restricted-senders"
	var senders []struct {
		MailboxID string `json:"mailboxId"`
		Reason    string `json:"reason"`
	}
	w.admin.must("GET", restricted, nil, &senders, 200)
	if len(senders) != 1 || senders[0].MailboxID != w.bobMailbox || senders[0].Reason != "messagesPerHour" {
		t.Fatalf("restricted senders %+v", senders)
	}
	waitFor(t, "the administrator notice", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", "Sending restricted for "+bob) != 0 })
	if findInAPI(w.alice, w.aliceMailbox, "INBOX", "Sending limit nearly reached by "+bob) == 0 {
		t.Fatal("no notice before the limit was reached")
	}
	var audit []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
	seen := map[string]bool{}
	for _, e := range audit {
		seen[e.Action] = true
	}
	if !seen["mailbox.sending_restricted"] || !seen["mailbox.sending_limit_near"] || !seen["sending_limits.update"] {
		t.Fatalf("audit log %v", seen)
	}

	if code := w.bob.do("DELETE", "/mailboxes/"+w.bobMailbox+"/sending-restriction", nil, nil); code != 403 && code != 404 {
		t.Fatalf("restricted sender released itself: %d", code)
	}
	w.admin.must("DELETE", "/mailboxes/"+w.bobMailbox+"/sending-restriction", nil, nil, 204)
	if err := send(alice); err != nil {
		t.Fatalf("released sender: %v", err)
	}
	w.admin.must("GET", restricted, nil, &senders, 200)
	if len(senders) != 0 {
		t.Fatalf("restricted senders after release %+v", senders)
	}
}

//go:build integration

package integration

import (
	"strings"
	"testing"
)

type senderEntryBody struct {
	ID       string  `json:"id"`
	DomainID *string `json:"domainId"`
	Action   string  `json:"action"`
	Kind     string  `json:"kind"`
	Value    string  `json:"value"`
}

func TestSenderLists(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	alice := "alice@" + w.domain
	base := "/organizations/" + w.orgID + "/sender-lists"

	if code := w.admin.do("POST", base, map[string]any{"action": "block", "value": "not a sender"}, nil); code != 422 {
		t.Fatalf("invalid sender: %d, want 422", code)
	}
	if code := w.alice.do("GET", base, nil, nil); code != 403 && code != 404 {
		t.Fatalf("member read the organization's sender lists: %d", code)
	}

	var blocked senderEntryBody
	w.admin.must("POST", base, map[string]any{"action": "block", "value": "@Phish.Example", "note": "campaign"}, &blocked, 200)
	if blocked.Kind != "domain" || blocked.Value != "phish.example" || blocked.DomainID != nil {
		t.Fatalf("blocked entry %+v", blocked)
	}
	err := submit(t, smtpAddr, "", "", "ceo@mail.phish.example", []string{alice}, rfc822("ceo@mail.phish.example", alice, "invoice", "x"), false)
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("blocked domain delivered: %v", err)
	}

	var allowed senderEntryBody
	w.admin.must("POST", base, map[string]any{"action": "allow", "value": "@phish.example", "domainId": w.domainID}, &allowed, 200)
	err = submit(t, smtpAddr, "", "", "ceo@phish.example", []string{alice}, rfc822("ceo@phish.example", alice, "invoice", "x"), false)
	if err == nil {
		t.Fatal("a domain allow overrode an organization block")
	}
	w.admin.must("DELETE", base+"/"+blocked.ID, nil, nil, 204)
	var entries []senderEntryBody
	w.admin.must("GET", base, nil, &entries, 200)
	if len(entries) != 1 || entries[0].ID != allowed.ID {
		t.Fatalf("entries after removal %+v", entries)
	}

	personal := "/mailboxes/" + w.aliceMailbox + "/sender-lists"
	if code := w.alice.do("POST", personal, map[string]any{"action": "block", "value": "203.0.113.9"}, nil); code != 422 {
		t.Fatalf("personal IP entry: %d, want 422", code)
	}
	w.alice.must("POST", personal, map[string]any{"action": "block", "value": "pest@external.example"}, &entries, 200)
	if len(entries) != 1 || entries[0].Action != "block" {
		t.Fatalf("personal entries %+v", entries)
	}
	if code := w.bob.do("GET", personal, nil, nil); code != 404 {
		t.Fatalf("another member read alice's sender list: %d", code)
	}

	blockedSubject, controlSubject := "pest "+randomSuffix(), "control "+randomSuffix()
	if err := submit(t, smtpAddr, "", "", "pest@external.example", []string{alice}, rfc822("pest@external.example", alice, blockedSubject, "x"), false); err != nil {
		t.Fatalf("personally blocked sender rejected during SMTP: %v", err)
	}
	if err := submit(t, smtpAddr, "", "", "friend@external.example", []string{alice}, rfc822("friend@external.example", alice, controlSubject, "x"), false); err != nil {
		t.Fatal(err)
	}
	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	waitFor(t, "control delivery", func() bool { return imapFindSubject(t, c, "INBOX", controlSubject) != nil })
	for _, folder := range []string{"INBOX", "Junk"} {
		if imapFindSubject(t, c, folder, blockedSubject) != nil {
			t.Fatalf("personally blocked message delivered to %s", folder)
		}
	}
	w.alice.must("DELETE", personal+"/"+entries[0].ID, nil, &entries, 200)
	if len(entries) != 0 {
		t.Fatalf("personal entries after removal %+v", entries)
	}

	var audit []struct {
		Action string `json:"action"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
	counts := map[string]int{}
	for _, e := range audit {
		counts[e.Action]++
	}
	if counts["sender_list.add"] != 3 || counts["sender_list.remove"] != 2 {
		t.Fatalf("sender list audit events %v", counts)
	}
}

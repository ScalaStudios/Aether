//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type quarantineItem struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	Access    string `json:"access"`
	Status    string `json:"status"`
	Subject   string `json:"subject"`
	Recipient string `json:"recipient"`
	Mailbox   string `json:"mailbox"`
}

type quarantineList struct {
	Items     []quarantineItem `json:"items"`
	Total     int              `json:"total"`
	Requested int              `json:"requested"`
}

func quarantine(t *testing.T, action string, score float64, symbols []string, from string, rcpt []string, raw string) int {
	t.Helper()
	var syms []map[string]any
	for _, s := range symbols {
		syms = append(syms, map[string]any{"name": s, "score": 5})
	}
	scan, _ := json.Marshal(map[string]any{"action": action, "score": score, "from": from, "rcpt": rcpt, "symbols": syms})
	req, _ := http.NewRequest("POST", env(t, "AETHER_TEST_API", "http://api:8080")+"/internal/quarantine", strings.NewReader(raw))
	req.Header.Set("X-Aether-Scan", string(scan))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post to quarantine: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func findHeld(items []quarantineItem, subject string) *quarantineItem {
	for i := range items {
		if items[i].Subject == subject {
			return &items[i]
		}
	}
	return nil
}

func TestQuarantine(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	if got := w.admin.do("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": alice, "role": "admin"}, nil); got >= 300 {
		t.Fatalf("make alice an administrator: status %d", got)
	}
	own := "/mailboxes/" + w.bobMailbox + "/quarantine"
	org := "/organizations/" + w.orgID + "/quarantine"
	held := func(c *apiClient, path string) quarantineList {
		var l quarantineList
		c.must("GET", path, nil, &l, 200)
		return l
	}

	t.Run("people release spam and allow the sender", func(t *testing.T) {
		sender := "promo-" + randomSuffix() + "@spam.test"
		subject := "spam " + randomSuffix()
		if got := quarantine(t, "add header", 12, []string{"BAYES_SPAM"}, sender, []string{bob}, rfc822(sender, bob, subject, `<a href="https://spam.test/buy">buy</a>`)); got != 201 {
			t.Fatalf("quarantine returned %d, want 201", got)
		}
		item := findHeld(held(w.bob, own).Items, subject)
		if item == nil || item.Category != "spam" || item.Access != "release" || item.Status != "held" {
			t.Fatalf("held item = %+v", item)
		}
		var preview struct {
			CanAllowSender bool `json:"canAllowSender"`
			Message        struct {
				Subject string `json:"subject"`
				Text    string `json:"text"`
			} `json:"message"`
		}
		w.bob.must("GET", own+"/"+item.ID, nil, &preview, 200)
		if preview.Message.Subject != subject || !preview.CanAllowSender {
			t.Fatalf("preview = %+v", preview)
		}
		w.alice.must("GET", own+"/"+item.ID, nil, nil, 404)
		w.bob.must("POST", own+"/"+item.ID+"/release", nil, nil, 204)
		if findInAPI(w.bob, w.bobMailbox, "INBOX", subject) == 0 {
			t.Fatal("released message is not in the inbox")
		}
		if findHeld(held(w.bob, own).Items, subject) != nil {
			t.Fatal("released message is still listed")
		}
		w.bob.must("POST", own+"/"+item.ID+"/release", nil, nil, 404)

		second := "spam again " + randomSuffix()
		quarantine(t, "add header", 12, []string{"BAYES_SPAM"}, sender, []string{bob}, rfc822(sender, bob, second, "more"))
		item = findHeld(held(w.bob, own).Items, second)
		w.bob.must("POST", own+"/"+item.ID+"/allow-sender", nil, nil, 204)
		var allowed []struct {
			Sender string `json:"sender"`
		}
		w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/allowed-senders", nil, &allowed, 200)
		if len(allowed) != 1 || allowed[0].Sender != sender {
			t.Fatalf("allowed senders = %+v", allowed)
		}

		third := "allowed " + randomSuffix()
		if got := quarantine(t, "add header", 12, []string{"BAYES_SPAM"}, sender, []string{bob}, rfc822(sender, bob, third, "now delivered")); got != 201 {
			t.Fatalf("quarantine returned %d", got)
		}
		if findInAPI(w.bob, w.bobMailbox, "INBOX", third) == 0 || findHeld(held(w.bob, own).Items, third) != nil {
			t.Fatal("mail from an allowed sender was held")
		}
		for _, c := range []struct {
			action, symbol, why string
		}{{"add header", "DMARC_POLICY_REJECT", "failing DMARC"}, {"reject", "BAYES_SPAM", "held as high-confidence spam, which people may only request"}} {
			subject := c.symbol + " " + randomSuffix()
			quarantine(t, c.action, 16, []string{c.symbol}, sender, []string{bob}, rfc822(sender, bob, subject, "held anyway"))
			if findHeld(held(w.bob, own).Items, subject) == nil {
				t.Fatalf("mail %s bypassed quarantine through an allowed sender", c.why)
			}
		}
		malware := "invoice " + randomSuffix()
		quarantine(t, "reject", 0, []string{"CLAM_VIRUS"}, sender, []string{bob}, rfc822(sender, bob, malware, "attached"))
		if findHeld(held(w.bob, own).Items, malware) != nil {
			t.Fatal("malware is visible to the recipient under the default policy")
		}
		if item := findHeld(held(w.alice, org).Items, malware); item == nil || item.Category != "malware" {
			t.Fatalf("administrator does not see the malware: %+v", item)
		}
		w.bob.must("DELETE", "/mailboxes/"+w.bobMailbox+"/allowed-senders?sender="+sender, nil, nil, 204)
	})

	t.Run("phishing is released by an administrator on request", func(t *testing.T) {
		sender := "it-" + randomSuffix() + "@phish.test"
		subject := "password expiry " + randomSuffix()
		quarantine(t, "reject", 18, []string{"PHISHING"}, sender, []string{bob}, rfc822(sender, bob, subject, "sign in"))
		item := findHeld(held(w.bob, own).Items, subject)
		if item == nil || item.Category != "phishing" || item.Access != "request" {
			t.Fatalf("held item = %+v", item)
		}
		w.bob.must("POST", own+"/"+item.ID+"/release", nil, nil, 403)
		w.bob.must("POST", own+"/"+item.ID+"/request", nil, nil, 204)
		requested := held(w.alice, org+"?status=requested")
		if findHeld(requested.Items, subject) == nil || requested.Requested < 1 {
			t.Fatalf("request not visible to the administrator: %+v", requested)
		}
		w.bob.must("GET", org, nil, nil, 403)
		w.admin.must("GET", org, nil, nil, 403)
		var result struct {
			Released int `json:"released"`
			Failed   int `json:"failed"`
		}
		w.alice.must("POST", org+"/release", map[string]any{"ids": []string{item.ID}}, &result, 200)
		if result.Released != 1 || result.Failed != 0 || findInAPI(w.bob, w.bobMailbox, "INBOX", subject) == 0 {
			t.Fatalf("bulk release = %+v", result)
		}
		if findHeld(held(w.alice, org+"?status=released").Items, subject) == nil {
			t.Fatal("released message is not listed as released")
		}
		var events []struct {
			Action   string `json:"action"`
			TargetID string `json:"targetId"`
		}
		w.alice.must("GET", "/organizations/"+w.orgID+"/audit", nil, &events, 200)
		var logged []string
		for _, e := range events {
			if e.TargetID == item.ID {
				logged = append(logged, e.Action)
			}
		}
		if strings.Join(logged, ",") != "quarantine.release,quarantine.request_release" {
			t.Fatalf("audit for the item = %v", logged)
		}
	})

	t.Run("lists hold one copy per member and policy changes apply", func(t *testing.T) {
		var list idOnly
		local := "team" + randomSuffix()
		w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": local, "destinations": []string{alice, bob, "outside@elsewhere.test"}}, &list, 201)
		subject := "to the team " + randomSuffix()
		quarantine(t, "reject", 20, []string{"CLAM_VIRUS"}, "x@malware.test", []string{local + "@" + w.domain, bob}, rfc822("x@malware.test", local+"@"+w.domain, subject, "payload"))
		copies := held(w.alice, org+"?category=malware&q="+url.QueryEscape(subject)).Items
		if len(copies) != 2 || (copies[0].Recipient != local+"@"+w.domain && copies[1].Recipient != local+"@"+w.domain) {
			t.Fatalf("copies = %+v", copies)
		}
		var policy map[string]any
		w.alice.must("GET", "/organizations/"+w.orgID+"/quarantine-policy", nil, &policy, 200)
		policy["access"].(map[string]any)["malware"] = "release"
		w.alice.must("PUT", "/organizations/"+w.orgID+"/quarantine-policy", policy, nil, 200)
		if item := findHeld(held(w.bob, own).Items, subject); item == nil || item.Access != "release" {
			t.Fatalf("policy change not applied: %+v", item)
		}
		var deleted struct {
			Deleted int `json:"deleted"`
		}
		w.alice.must("POST", org+"/delete", map[string]any{"ids": []string{copies[0].ID, copies[1].ID}}, &deleted, 200)
		if deleted.Deleted != 2 || findHeld(held(w.bob, own).Items, subject) != nil {
			t.Fatalf("delete = %+v", deleted)
		}
		if got := quarantine(t, "reject", 20, nil, "x@spam.test", []string{"nobody@unhosted.test"}, rfc822("x@spam.test", "nobody@unhosted.test", "x", "x")); got != 204 {
			t.Fatalf("mail without hosted mailboxes returned %d, want 204", got)
		}
	})

	t.Run("digest arrives in the inbox", func(t *testing.T) {
		waitUpTo(t, time.Minute, "quarantine digest", func() bool {
			var page messagePage
			w.bob.must("GET", "/mailboxes/"+w.bobMailbox+"/messages?folder=INBOX", nil, &page, 200)
			for _, m := range page.Messages {
				if strings.HasSuffix(m.Subject, "in quarantine") {
					return true
				}
			}
			return false
		})
	})
}

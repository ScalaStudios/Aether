//go:build integration

package integration

import (
	"bytes"
	"slices"
	"strconv"
	"testing"
)

type senderView struct {
	Sender *struct {
		Organization string `json:"organization"`
		External     bool   `json:"external"`
		FirstContact bool   `json:"firstContact"`
		Warnings     []struct {
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Address   string `json:"address"`
			Domain    string `json:"domain"`
			Resembles string `json:"resembles"`
		} `json:"warnings"`
		Authentication *struct {
			Submission string `json:"submission"`
			SPF        struct {
				Result string `json:"result"`
			} `json:"spf"`
			DMARC struct {
				Result string `json:"result"`
			} `json:"dmarc"`
		} `json:"authentication"`
	} `json:"sender"`
}

func (v senderView) kinds() []string {
	var out []string
	for _, w := range v.Sender.Warnings {
		out = append(out, w.Kind)
	}
	return out
}

func TestSenderWarnings(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	forged := "X-Aether-Authentication: auth=pass spf=pass dkim=pass dmarc=pass\r\n"

	deliver := func(t *testing.T, folder, addr, user, pass, from, header string) senderView {
		t.Helper()
		subject := "sender " + randomSuffix()
		if err := submit(t, addr, user, pass, from, []string{alice}, forged+rfc822(header, alice, subject, "hello"), false); err != nil {
			t.Fatalf("deliver from %s: %v", from, err)
		}
		var uid uint32
		waitFor(t, "the message to arrive", func() bool {
			uid = findInAPI(w.alice, w.aliceMailbox, folder, subject)
			return uid != 0
		})
		var v senderView
		w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/messages/"+strconv.FormatUint(uint64(uid), 10)+"?folder="+folder, nil, &v, 200)
		if v.Sender == nil || v.Sender.Authentication == nil {
			t.Fatalf("no trusted sender assessment for %s: %+v", from, v.Sender)
		}
		return v
	}

	t.Run("mail from a colleague is internal and forged results are replaced", func(t *testing.T) {
		bobPass := createAppPassword(t, w.bob, w.bobMailbox)
		subject := "internal " + randomSuffix()
		if err := submit(t, submissionAddr, bob, bobPass, bob, []string{alice}, "X-Aether-Authentication: auth=none spf=fail dkim=fail dmarc=fail\r\n"+rfc822(bob, alice, subject, "hi"), false); err != nil {
			t.Fatalf("submission: %v", err)
		}
		ic, err := imapLogin(t, alice, w.aliceAppPassword)
		if err != nil {
			t.Fatal(err)
		}
		defer ic.Close()
		var raw []byte
		waitFor(t, "alice to receive bob's message", func() bool {
			raw = imapFindSubject(t, ic, "INBOX", subject)
			return raw != nil
		})
		if n := bytes.Count(raw, []byte("X-Aether-Authentication:")); n != 1 || !bytes.Contains(raw, []byte("X-Aether-Authentication: auth=pass")) {
			t.Fatalf("want exactly one trusted header with auth=pass, got %d:\n%s", n, raw)
		}
		var v senderView
		uid := findInAPI(w.alice, w.aliceMailbox, "INBOX", subject)
		w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/messages/"+strconv.FormatUint(uint64(uid), 10)+"?folder=INBOX", nil, &v, 200)
		if v.Sender == nil || v.Sender.External || v.Sender.FirstContact || len(v.Sender.Warnings) != 0 || v.Sender.Authentication.Submission != "pass" {
			t.Fatalf("colleague mail should be internal with no warnings: %+v", v.Sender)
		}
	})

	t.Run("outside sender using a colleague's name", func(t *testing.T) {
		v := deliver(t, "INBOX", smtpAddr, "", "", "bob.personal@partner.example", "Bob <bob.personal@partner.example>")
		if !v.Sender.External || !v.Sender.FirstContact || v.Sender.Authentication.Submission != "none" {
			t.Fatalf("want external first contact with forged results replaced: %+v", v.Sender)
		}
		if !slices.Equal(v.kinds(), []string{"name"}) || v.Sender.Warnings[0].Address != bob {
			t.Fatalf("want a display name warning pointing at %s: %+v", bob, v.Sender.Warnings)
		}
		again := deliver(t, "INBOX", smtpAddr, "", "", "bob.personal@partner.example", "Bob <bob.personal@partner.example>")
		if again.Sender.FirstContact {
			t.Fatal("a second message from the same sender is still a first contact")
		}
	})

	t.Run("outside sender claiming the organization's domain", func(t *testing.T) {
		v := deliver(t, "Junk", smtpAddr, "", "", "ceo@"+w.domain, "Chief <ceo@"+w.domain+">")
		if !v.Sender.External || !slices.Contains(v.kinds(), "spoofed") || v.Sender.FirstContact {
			t.Fatalf("want a spoofing warning without a first contact tip: %+v", v.Sender)
		}
	})

	t.Run("outside sender on a look-alike domain", func(t *testing.T) {
		lookalike := w.domain[1:2] + w.domain[:1] + w.domain[2:]
		v := deliver(t, "INBOX", smtpAddr, "", "", "billing@"+lookalike, "Billing <billing@"+lookalike+">")
		if !slices.Contains(v.kinds(), "lookalike") || v.Sender.Warnings[slices.Index(v.kinds(), "lookalike")].Resembles != w.domain {
			t.Fatalf("want a look-alike warning for %s: %+v", lookalike, v.Sender.Warnings)
		}
	})

	t.Run("administrators can turn the warnings off", func(t *testing.T) {
		path := "/organizations/" + w.orgID + "/sender-warnings"
		if status := w.alice.do("GET", path, nil, nil); status != 403 && status != 404 {
			t.Fatalf("a member read the settings: %d", status)
		}
		var settings map[string]bool
		w.admin.must("GET", path, nil, &settings, 200)
		if !settings["externalTag"] || !settings["firstContact"] || !settings["impersonation"] {
			t.Fatalf("warnings should be on by default: %v", settings)
		}
		w.admin.must("PUT", path, map[string]bool{"externalTag": false, "firstContact": false, "impersonation": false}, &settings, 200)
		v := deliver(t, "INBOX", smtpAddr, "", "", "someone@elsewhere.example", "Bob <someone@elsewhere.example>")
		if v.Sender.External || v.Sender.FirstContact || len(v.Sender.Warnings) != 0 || v.Sender.Authentication.Submission != "none" {
			t.Fatalf("turned off warnings still show: %+v", v.Sender)
		}
		var audit []struct {
			Action string `json:"action"`
		}
		w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
		if !slices.ContainsFunc(audit, func(e struct {
			Action string `json:"action"`
		}) bool {
			return e.Action == "sender_warnings.update"
		}) {
			t.Fatal("the settings change is missing from the audit log")
		}
	})
}

//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"testing"
	"time"
)

type aliasBody struct {
	ID             string   `json:"id"`
	Address        string   `json:"address"`
	Kind           string   `json:"kind"`
	Destinations   []string `json:"destinations"`
	Active         bool     `json:"active"`
	AcceptFrom     string   `json:"acceptFrom"`
	SendAs         []string `json:"sendAs"`
	Owners         []string `json:"owners"`
	AllowedSenders []string `json:"allowedSenders"`
}

func TestAliasUpdateAndDistributionLists(t *testing.T) {
	w := setupWorld(t)
	smtpAddr := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain

	var a aliasBody
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "privacy", "destinations": []string{alice}}, &a, 201)
	var updated aliasBody
	w.admin.must("PATCH", "/aliases/"+a.ID, map[string]any{"addDestinations": []string{bob}, "removeDestinations": []string{alice}}, &updated, 200)
	if updated.ID != a.ID || len(updated.Destinations) != 1 || updated.Destinations[0] != bob {
		t.Fatalf("PATCH destinations: %+v", updated)
	}
	w.admin.must("PATCH", "/aliases/"+a.ID, map[string]any{"active": false}, &updated, 200)
	if updated.Active {
		t.Fatal("PATCH active=false left the alias active")
	}
	if code := w.admin.do("PATCH", "/aliases/"+a.ID, map[string]any{"acceptFrom": "members"}, nil); code != 422 {
		t.Fatalf("sender restriction on a plain alias: %d, want 422", code)
	}
	if code := w.alice.do("PATCH", "/aliases/"+a.ID, map[string]any{"active": true}, nil); code != 403 && code != 404 {
		t.Fatalf("member edited an alias: %d", code)
	}

	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	w.bob.must("GET", "/session", nil, &me, 200)
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": bob, "role": "member"}, nil, 204)

	var list aliasBody
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{
		"domainId": w.domainID, "localPart": "staff", "kind": "list", "name": "Staff",
		"destinations": []string{alice}, "acceptFrom": "organization", "sendAs": []string{bob}, "owners": []string{me.User.ID},
	}, &list, 201)
	if list.Kind != "list" || list.AcceptFrom != "organization" || len(list.Owners) != 1 {
		t.Fatalf("created list %+v", list)
	}

	var owned []aliasBody
	w.bob.must("GET", "/me/lists", nil, &owned, 200)
	if len(owned) != 1 || owned[0].ID != list.ID {
		t.Fatalf("owner lists %+v", owned)
	}
	w.bob.must("PATCH", "/aliases/"+list.ID, map[string]any{"addDestinations": []string{bob}}, &updated, 200)
	if code := w.bob.do("PATCH", "/aliases/"+list.ID, map[string]any{"acceptFrom": "anyone"}, nil); code != 403 {
		t.Fatalf("owner changed sender restriction: %d, want 403", code)
	}

	var dir []struct {
		Address string `json:"address"`
	}
	w.alice.must("GET", "/directory?q=staff", nil, &dir, 200)
	if len(dir) != 1 || dir[0].Address != list.Address {
		t.Fatalf("directory %+v", dir)
	}

	err := submit(t, smtpAddr, "", "", "outsider@external.example", []string{list.Address}, rfc822("outsider@external.example", list.Address, "outside", "x"), false)
	if err == nil || !strings.Contains(err.Error(), "does not accept mail") {
		t.Fatalf("outside sender to an organization-only list: %v", err)
	}
	if err := submit(t, smtpAddr, "", "", alice, []string{list.Address}, rfc822(alice, list.Address, "spoof", "x"), false); err == nil {
		t.Fatal("unauthenticated sender claiming a hosted address reached an organization-only list")
	}
	subject := "list " + randomSuffix()
	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, alice, []string{list.Address}, rfc822(alice, list.Address, subject, "x"), false); err != nil {
		t.Fatalf("member sending to the list: %v", err)
	}
	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	waitFor(t, "list delivery", func() bool { return imapFindSubject(t, c, "INBOX", subject) != nil })

	if err := submit(t, submissionAddr, alice, w.aliceAppPassword, list.Address, []string{bob}, rfc822(list.Address, bob, "as list", "x"), false); err == nil {
		t.Fatal("a member without send-as permission sent as the list")
	}

	w.admin.must("PATCH", "/aliases/"+list.ID, map[string]any{"acceptFrom": "allowlist", "allowedSenders": []string{"@partner.example"}}, &updated, 200)
	if err := submit(t, smtpAddr, "", "", "ceo@partner.example", []string{list.Address}, rfc822("ceo@partner.example", list.Address, "partner", "x"), false); err != nil {
		t.Fatalf("allow-listed domain rejected: %v", err)
	}

	removed := newClient(t)
	removed.login(bob, "bob-integration-pw")
	w.admin.must("DELETE", "/organizations/"+w.orgID+"/members/"+me.User.ID, nil, nil, 204)
	w.bob.must("GET", "/me/lists", nil, &owned, 200)
	if len(owned) != 0 {
		t.Fatalf("removed member still owns %+v", owned)
	}
	w.admin.must("DELETE", "/aliases/"+list.ID, nil, nil, 204)
}

func TestWebmailSenderIdentities(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	path := "/mailboxes/" + w.aliceMailbox
	shared := createSharedMailbox(t, w)
	setDelegate(t, w.admin, shared.ID, map[string]any{"email": alice, "access": "read", "sendOnBehalf": true})
	var alias, list aliasBody
	aliases := "/organizations/" + w.orgID + "/aliases"
	w.admin.must("POST", aliases, map[string]any{"domainId": w.domainID, "localPart": "help", "destinations": []string{alice}}, &alias, 201)
	w.admin.must("POST", aliases, map[string]any{"domainId": w.domainID, "localPart": "staff", "kind": "list", "name": "Staff", "destinations": []string{bob}, "sendAs": []string{alice}, "sentMailboxId": shared.ID}, &list, 201)
	w.admin.must("POST", aliases, map[string]any{"domainId": w.domainID, "localPart": "", "destinations": []string{alice}}, nil, 201)

	type address struct{ Name, Address string }
	fromAddresses := func(c *apiClient, mailboxID string) []address {
		t.Helper()
		var boxes []struct {
			ID            string
			FromAddresses []address
		}
		c.must("GET", "/me/mailboxes", nil, &boxes, 200)
		for _, box := range boxes {
			if box.ID == mailboxID {
				return box.FromAddresses
			}
		}
		t.Fatalf("missing mailbox %s", mailboxID)
		return nil
	}
	sigPath := path + "/signatures?fromAddress=" + alias.Address
	settings := map[string]any{"displayName": "Help Desk", "newMessage": "team", "replies": "team", "signatures": []map[string]string{{"id": "team", "name": "Team", "html": "<p>Support team</p>"}}}
	w.alice.must("PUT", sigPath, settings, nil, 200)
	var profile struct {
		DisplayName, NewMessage, Replies string
		Signatures                       []struct{ HTML string }
	}
	w.alice.must("GET", sigPath, nil, &profile, 200)
	if profile.DisplayName != "Help Desk" || profile.Replies != "team" || profile.NewMessage != "team" || len(profile.Signatures) != 1 || profile.Signatures[0].HTML != "<p>Support team</p>" {
		t.Fatalf("alias profile: %+v", profile)
	}
	var primary struct {
		DisplayName string
		Signatures  []struct{ HTML string }
	}
	w.alice.must("GET", path+"/signatures", nil, &primary, 200)
	if primary.DisplayName != "" || len(primary.Signatures) != 0 {
		t.Fatalf("alias changed primary profile: %+v", primary)
	}
	var template struct{ HTML string }
	w.alice.must("GET", path+"/signature-template?fromAddress="+alias.Address, nil, &template, 200)
	if !strings.Contains(template.HTML, alias.Address) || !strings.Contains(template.HTML, "Help Desk") {
		t.Fatalf("template: %s", template.HTML)
	}
	if got := fromAddresses(w.alice, w.aliceMailbox); !slices.Equal(got, []address{{"Alice", alice}, {"Help Desk", alias.Address}, {"Staff", list.Address}}) {
		t.Fatalf("alice identities: %+v", got)
	}
	if got := fromAddresses(w.bob, w.bobMailbox); !slices.Equal(got, []address{{"Bob", bob}}) {
		t.Fatalf("member identities: %+v", got)
	}
	if got := fromAddresses(w.alice, shared.ID); !slices.Equal(got, []address{{"Support", shared.Address}}) {
		t.Fatalf("shared identities: %+v", got)
	}

	incoming := "blind to alias " + randomSuffix()
	if err := submit(t, env(t, "AETHER_TEST_SUBMISSION", "postfix:587"), alice, w.aliceAppPassword, alice, []string{alias.Address}, rfc822(alice, bob, incoming, "question"), false); err != nil {
		t.Fatal(err)
	}
	var uid uint32
	waitFor(t, "blind delivery to alias", func() bool { uid = findInAPI(w.alice, w.aliceMailbox, "INBOX", incoming); return uid != 0 })
	var received struct{ DeliveredTo, MessageID string }
	w.alice.must("GET", path+"/messages/"+itoaUID(uid), nil, &received, 200)
	if received.DeliveredTo != alias.Address {
		t.Fatalf("blind delivery address %q, want %q", received.DeliveredTo, alias.Address)
	}

	message := map[string]any{"fromAddress": alias.Address, "to": []address{{Address: bob}}, "subject": "alias reply " + randomSuffix(), "html": "<p>Answer</p><p>Support team</p>", "inReplyTo": received.MessageID}
	var draft struct {
		UID    uint32
		Folder string
	}
	w.alice.must("PUT", path+"/drafts", message, &draft, 200)
	var saved struct{ From []address }
	w.alice.must("GET", path+"/messages/"+itoaUID(draft.UID)+"?folder="+draft.Folder, nil, &saved, 200)
	if !slices.Equal(saved.From, []address{{"Help Desk", alias.Address}}) {
		t.Fatalf("draft from: %+v", saved.From)
	}
	message["draftUid"] = draft.UID
	w.alice.must("POST", path+"/send", message, nil, 202)
	ic, err := imapLogin(t, bob, createAppPassword(t, w.bob, w.bobMailbox))
	if err != nil {
		t.Fatal(err)
	}
	defer ic.Close()
	checkDelivery := func(subject, name, from string) {
		t.Helper()
		var raw []byte
		waitFor(t, "delivery of "+subject, func() bool { raw = imapFindSubject(t, ic, "INBOX", subject); return raw != nil })
		msg, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		got, err := mail.ParseAddress(msg.Header.Get("From"))
		if err != nil || got.Name != name || got.Address != from || msg.Header.Get("Return-Path") != "<"+from+">" || msg.Header.Get("Sender") != "" {
			t.Fatalf("delivered headers: %+v", msg.Header)
		}
	}
	subject := message["subject"].(string)
	checkDelivery(subject, "Help Desk", alias.Address)
	if findInAPI(w.alice, w.aliceMailbox, "Sent", subject) == 0 || findInAPI(w.alice, w.aliceMailbox, draft.Folder, subject) != 0 {
		t.Fatal("alias reply was not moved from Drafts to personal Sent")
	}
	delete(message, "draftUid")
	message["fromAddress"] = list.Address
	message["subject"] = "scheduled as list " + randomSuffix()
	message["sendAt"] = time.Now().Add(8 * time.Second)
	w.bob.must("POST", "/mailboxes/"+w.bobMailbox+"/send", message, nil, 403)
	w.alice.must("POST", path+"/send", message, nil, 202)
	subject = message["subject"].(string)
	checkDelivery(subject, "Staff", list.Address)
	waitFor(t, "list Sent copy", func() bool { return findInAPI(w.alice, shared.ID, "Sent", subject) != 0 })
	if findInAPI(w.alice, w.aliceMailbox, "Sent", subject) != 0 {
		t.Fatal("list Sent copy duplicated in personal Sent")
	}

	message["subject"] = "revoked alias " + randomSuffix()
	message["fromAddress"] = alias.Address
	message["sendAt"] = time.Now().Add(8 * time.Second)
	w.alice.must("POST", path+"/send", message, nil, 202)
	w.admin.must("PATCH", "/aliases/"+alias.ID, map[string]bool{"active": false}, nil, 200)
	w.alice.must("GET", sigPath, nil, nil, 403)
	delete(message, "sendAt")
	w.alice.must("POST", path+"/send", message, nil, 403)
	w.alice.must("PUT", path+"/drafts", message, nil, 403)
	if slices.Contains(fromAddresses(w.alice, w.aliceMailbox), address{"Help Desk", alias.Address}) {
		t.Fatal("disabled alias remains selectable")
	}
	waitFor(t, "revoked scheduled sender", func() bool {
		var pending []struct {
			Error   string
			Subject string
		}
		w.alice.must("GET", path+"/scheduled", nil, &pending, 200)
		return len(pending) == 1 && pending[0].Subject == message["subject"] && strings.Contains(pending[0].Error, "permission")
	})
	if findInAPI(w.bob, w.bobMailbox, "INBOX", message["subject"].(string)) != 0 {
		t.Fatal("revoked scheduled sender delivered mail")
	}
}

func TestAPIKeyMailboxManagement(t *testing.T) {
	w := setupWorld(t)
	var created struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{"name": "migration", "scopes": []string{"mailboxes:write", "aliases:write"}}, &created, 201)
	key := &apiClient{t: t, base: w.admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: created.Token}

	key.must("POST", "/mailboxes/"+w.aliceMailbox+"/delegates", map[string]string{"email": "bob@" + w.domain, "access": "read"}, nil, 200)
	var delegates []struct {
		Access string `json:"access"`
	}
	key.must("GET", "/mailboxes/"+w.aliceMailbox+"/delegates", nil, &delegates, 200)
	if len(delegates) != 1 || delegates[0].Access != "read" {
		t.Fatalf("delegates %+v", delegates)
	}

	rules := []map[string]any{{
		"name": "automatic replies", "enabled": true, "match": "all",
		"conditions": []map[string]any{{"field": "body", "contains": "automatically generated"}, {"field": "header", "header": "X-Mailer", "contains": "bot", "negate": true}},
		"actions":    map[string]any{"delete": true},
	}}
	key.must("PUT", "/mailboxes/"+w.aliceMailbox+"/rules", rules, nil, 200)
	key.must("PUT", "/mailboxes/"+w.aliceMailbox+"/auto-reply", map[string]any{"enabled": true, "message": "Away"}, nil, 200)
	key.must("PUT", "/mailboxes/"+w.aliceMailbox+"/signatures", map[string]any{"signatures": []map[string]string{{"id": "main", "name": "Main", "html": "<p>Alice</p>"}}, "newMessage": "main"}, nil, 200)
	var got []struct {
		Name string `json:"name"`
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/rules", nil, &got, 200)
	if len(got) != 1 {
		t.Fatalf("rules set by the API key are not visible to the owner: %+v", got)
	}

	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//it//EN\r\nBEGIN:VEVENT\r\nUID:migrated-" + randomSuffix() + "\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20261101T090000Z\r\nDTEND:20261101T100000Z\r\nSUMMARY:Migrated\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	var counts map[string]int
	key.mustRaw("POST", "/mailboxes/"+w.aliceMailbox+"/calendar/import", "text/calendar", ics, &counts, 200)
	if counts["imported"] != 1 {
		t.Fatalf("calendar import %+v", counts)
	}
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c-" + randomSuffix() + "\r\nFN:Carol Example\r\nEMAIL:carol@example.org\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nFN:Dan Example\r\nEMAIL:dan@example.org\r\nEND:VCARD\r\n"
	key.mustRaw("POST", "/mailboxes/"+w.aliceMailbox+"/contacts/import", "text/vcard", vcf, &counts, 200)
	if counts["imported"] != 2 {
		t.Fatalf("contacts import %+v", counts)
	}
	var contacts []struct {
		Name string `json:"name"`
	}
	w.alice.must("GET", "/contacts?q=example.org", nil, &contacts, 200)
	if len(contacts) != 2 {
		t.Fatalf("alice sees %d imported contacts, want 2", len(contacts))
	}

	key.must("DELETE", "/mailboxes/"+w.aliceMailbox+"/delegates/"+userID(t, w.bob), nil, nil, 204)
	if code := key.do("GET", "/mailboxes/"+w.aliceMailbox+"/rules", nil, nil); code != 200 {
		t.Fatalf("mailboxes:write key reading rules: %d", code)
	}
}

func userID(t *testing.T, c *apiClient) string {
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	c.must("GET", "/session", nil, &me, 200)
	return me.User.ID
}

func TestPersonalAliasMailLifecycle(t *testing.T) {
	w := setupWorld(t)
	base := "/organizations/" + w.orgID
	policy := map[string]any{"limitPerUser": 1, "domainIds": []string{w.domainID}}
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	target := map[string]any{"mailboxId": w.aliceMailbox, "domainId": w.domainID, "localPart": "conference", "note": "Conference signup"}
	w.alice.must("POST", base+"/personal-aliases", target, nil, 403)
	w.alice.must("PUT", base+"/personal-alias-policy", policy, nil, 403)
	w.admin.must("PUT", base+"/personal-alias-policy", policy, nil, 204)
	w.bob.must("POST", base+"/personal-aliases", target, nil, 403)
	for _, local := range []string{"alice", "dmarc-reports", "bob+tag"} {
		target["localPart"] = local
		want := 422
		if local == "alice" {
			want = 409
		}
		w.alice.must("POST", base+"/personal-aliases", target, nil, want)
	}
	target["localPart"] = "conference"
	var a struct {
		aliasBody
		Note, PersonalOwnerID string
	}
	w.alice.must("POST", base+"/personal-aliases", target, &a, 201)
	if a.Address != "conference@"+w.domain || len(a.Destinations) != 1 || a.Destinations[0] != alice || a.Note != "Conference signup" || a.PersonalOwnerID == "" {
		t.Fatalf("personal alias: %+v", a)
	}
	path := "/aliases/" + a.ID
	w.bob.must("GET", path, nil, nil, 403)
	w.bob.must("PATCH", path, map[string]bool{"active": false}, nil, 403)
	w.bob.must("DELETE", path, nil, nil, 403)
	w.alice.must("PATCH", path, map[string]any{"destinations": []string{bob}}, nil, 400)
	w.admin.must("POST", base+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "", "destinations": []string{bob}}, nil, 201)
	smtp := env(t, "AETHER_TEST_SMTP", "postfix:25")
	submission := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	deliver := func() {
		t.Helper()
		subject := "personal alias " + randomSuffix()
		if err := submit(t, smtp, "", "", "outside@example.org", []string{a.Address}, rfc822("outside@example.org", a.Address, subject, "Mail for Alice"), false); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "personal alias delivery through IMAP", func() bool { return findInAPI(w.alice, w.aliceMailbox, "INBOX", subject) > 0 })
		if err := submit(t, submission, alice, w.aliceAppPassword, a.Address, []string{bob}, rfc822(a.Address, bob, subject, "Reply"), false); err != nil {
			t.Fatalf("send as personal alias: %v", err)
		}
	}
	rejected := func() {
		t.Helper()
		for _, recipient := range []string{a.Address, "conference+tag@" + w.domain} {
			for _, endpoint := range []string{smtp, submission} {
				from, user, pass := "outside@example.org", "", ""
				if endpoint == submission {
					from, user, pass = alice, alice, w.aliceAppPassword
				}
				err := submit(t, endpoint, user, pass, from, []string{recipient}, rfc822(from, recipient, "blocked", "x"), false)
				if err == nil || !strings.Contains(err.Error(), "5.1.1") && !strings.Contains(err.Error(), "Address unavailable") {
					t.Fatalf("%s accepted or did not permanently reject %s: %v", endpoint, recipient, err)
				}
			}
		}
		if err := submit(t, submission, alice, w.aliceAppPassword, a.Address, []string{bob}, rfc822(a.Address, bob, "blocked sender", "x"), false); err == nil || !strings.Contains(err.Error(), "not owned") {
			t.Fatalf("unavailable sender: %v", err)
		}
	}
	deliver()
	w.alice.must("PATCH", path, map[string]any{"active": false, "note": "Disabled after signup"}, nil, 200)
	w.alice.must("POST", base+"/personal-aliases", target, nil, 409)
	rejected()
	w.alice.must("PATCH", path, map[string]bool{"active": true}, nil, 200)
	deliver()
	expiry := time.Now().Add(2 * time.Second)
	w.alice.must("PATCH", path, map[string]any{"expiresAt": expiry}, nil, 200)
	waitFor(t, "alias expiry", func() bool { return time.Now().After(expiry) })
	rejected()
	w.alice.must("PATCH", path, map[string]any{"expiresAt": nil}, nil, 200)
	deliver()
	var items []aliasBody
	w.admin.must("GET", base+"/aliases", nil, &items, 200)
	found := false
	for _, item := range items {
		if item.ID == a.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("administrator could not see personal alias")
	}
	w.admin.must("DELETE", path, nil, nil, 204)
	w.alice.must("GET", path, nil, nil, 404)
	var events []struct{ Action, TargetID string }
	w.admin.must("GET", base+"/audit", nil, &events, 200)
	counts := map[string]int{}
	for _, e := range events {
		if e.TargetID == a.ID || e.TargetID == w.orgID {
			counts[e.Action]++
		}
	}
	if counts["alias.create"] != 1 || counts["alias.update"] != 4 || counts["alias.delete"] != 1 || counts["alias.policy.update"] != 1 {
		t.Fatalf("missing audit events: %v", counts)
	}
}

func TestMaskedAliasKeyAndQuota(t *testing.T) {
	w := setupWorld(t)
	base := "/organizations/" + w.orgID
	policy := map[string]any{"limitPerUser": 2, "domainIds": []string{w.domainID}}
	w.admin.must("PUT", base+"/personal-alias-policy", policy, nil, 204)
	target := map[string]string{"domainId": w.domainID, "mailboxId": w.aliceMailbox}
	var key struct{ Token string }
	w.bob.must("PUT", base+"/personal-alias-key", target, nil, 403)
	w.alice.must("PUT", base+"/personal-alias-key", target, &key, 200)
	generate := func(token, body string) (int, string) {
		req, err := http.NewRequest("POST", strings.TrimSuffix(w.alice.base, "/v1")+"/alias/random/new?hostname=shop.example", strings.NewReader(body))
		if err != nil {
			t.Error(err)
			return 0, ""
		}
		req.Header.Set("Authentication", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return 0, ""
		}
		defer resp.Body.Close()
		if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Error("masked alias response is not readable cross-origin")
		}
		var result struct{ Alias, Email string }
		if resp.StatusCode == 201 {
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			if result.Alias == "" || result.Alias != result.Email {
				t.Errorf("SimpleLogin clients read alias, got %+v", result)
			}
		}
		return resp.StatusCode, result.Email
	}
	check := func(token, body string, want int) {
		t.Helper()
		if code, _ := generate(token, body); code != want {
			t.Fatalf("generate status %d, want %d", code, want)
		}
	}
	preflight, err := http.NewRequest("OPTIONS", strings.TrimSuffix(w.alice.base, "/v1")+"/alias/random/new", nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight.Header.Set("Origin", "https://vault.bitwarden.com")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", "authentication,content-type")
	resp, err := http.DefaultClient.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "Authentication") {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	check("", "", 401)
	check(w.aliceAppPassword, "", 401)
	check(key.Token, `{"destination":"somebody@example.org"}`, 400)
	old := key.Token
	w.alice.must("PUT", base+"/personal-alias-key", target, &key, 200)
	check(old, "", 401)
	type result struct {
		code    int
		address string
	}
	results := make(chan result, 6)
	for range cap(results) {
		go func() { code, address := generate(key.Token, ""); results <- result{code, address} }()
	}
	addresses := map[string]bool{}
	for range cap(results) {
		res := <-results
		switch res.code {
		case 201:
			if addresses[res.address] || !strings.HasSuffix(res.address, "@"+w.domain) || strings.Contains(res.address, "alice") {
				t.Fatalf("invalid masked address %q", res.address)
			}
			addresses[res.address] = true
		case 409:
		default:
			t.Fatalf("unexpected generate status %d", res.code)
		}
	}
	if len(addresses) != 2 {
		t.Fatalf("concurrent quota allowed %d aliases", len(addresses))
	}
	var list struct {
		Aliases []struct{ ID, Address, Note string }
	}
	w.alice.must("GET", base+"/personal-aliases", nil, &list, 200)
	if len(list.Aliases) != 2 || list.Aliases[0].Note != "shop.example" {
		t.Fatalf("masked aliases: %+v", list)
	}
	w.alice.must("DELETE", "/aliases/"+list.Aliases[0].ID, nil, nil, 204)
	w.bob.must("GET", base+"/personal-aliases", nil, &list, 200)
	if len(list.Aliases) != 0 {
		t.Fatal("another member can see private aliases")
	}
	target["mailboxId"] = w.bobMailbox
	w.bob.must("POST", base+"/personal-aliases", target, nil, 201)
	policy["domainIds"] = []string{}
	w.admin.must("PUT", base+"/personal-alias-policy", policy, nil, 204)
	check(key.Token, "", 403)
	policy["domainIds"] = []string{w.domainID}
	w.admin.must("PUT", base+"/personal-alias-policy", policy, nil, 204)
	check(key.Token, `{"note":"Explicit note"}`, 201)
	w.alice.must("DELETE", base+"/personal-alias-key", nil, nil, 204)
	check(key.Token, "", 401)
}

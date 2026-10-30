//go:build integration

package integration

import (
	"net/http"
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

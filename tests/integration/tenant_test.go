//go:build integration

package integration

import "testing"

func TestTenantIsolation(t *testing.T) {
	a := setupWorld(t)
	b := setupWorld(t)
	a.admin.must("POST", "/organizations/"+a.orgID+"/members", map[string]string{"email": "alice@" + a.domain, "role": "admin"}, nil, 204)
	orgAdmin := newClient(t)
	orgAdmin.login("alice@"+a.domain, "alice-integration-pw")

	var orgs []idOnly
	orgAdmin.must("GET", "/organizations", nil, &orgs, 200)
	if len(orgs) != 1 || orgs[0].ID != a.orgID {
		t.Fatalf("org admin sees organizations %v, want only %s", orgs, a.orgID)
	}
	orgAdmin.must("GET", "/organizations/"+a.orgID+"/mailboxes", nil, nil, 200)

	denied := []struct{ method, path string }{
		{"GET", "/organizations/" + b.orgID},
		{"GET", "/organizations/" + b.orgID + "/domains"},
		{"GET", "/organizations/" + b.orgID + "/mailboxes"},
		{"GET", "/organizations/" + b.orgID + "/members"},
		{"GET", "/organizations/" + b.orgID + "/audit"},
		{"GET", "/domains/" + b.domainID},
		{"GET", "/domains/" + b.domainID + "/dns"},
		{"POST", "/domains/" + b.domainID + "/verify"},
		{"DELETE", "/domains/" + b.domainID},
		{"PATCH", "/mailboxes/" + b.bobMailbox},
		{"DELETE", "/mailboxes/" + b.bobMailbox},
		{"GET", "/mailboxes/" + b.bobMailbox + "/folders"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/messages"},
		{"POST", "/mailboxes/" + b.bobMailbox + "/send"},
		{"PUT", "/mailboxes/" + b.bobMailbox + "/drafts"},
		{"DELETE", "/mailboxes/" + b.bobMailbox + "/drafts/1"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/rules"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/auto-reply"},
	}
	for _, d := range denied {
		var body any
		if d.method == "PUT" {
			body = map[string]any{"subject": "x"}
		}
		if d.method == "PATCH" {
			body = map[string]bool{"active": false}
		}
		if d.method == "POST" && d.path[len(d.path)-4:] == "send" {
			body = map[string]any{"to": []map[string]string{{"address": "x@" + b.domain}}, "subject": "x"}
		}
		if got := orgAdmin.do(d.method, d.path, body, nil); got != 404 {
			t.Errorf("%s %s returned %d, want 404", d.method, d.path, got)
		}
	}

	if got := orgAdmin.do("POST", "/organizations/"+a.orgID+"/mailboxes", map[string]any{"domainId": b.domainID, "localPart": "intruder"}, nil); got != 422 {
		t.Errorf("creating a mailbox in another tenant's domain returned %d, want 422", got)
	}
	if got := orgAdmin.do("POST", "/organizations/"+a.orgID+"/aliases", map[string]any{"domainId": b.domainID, "localPart": "", "destinations": []string{"alice@" + a.domain}}, nil); got != 422 {
		t.Errorf("creating a catch-all in another tenant's domain returned %d, want 422", got)
	}
	if got := orgAdmin.do("POST", "/me/app-passwords", map[string]string{"name": "steal", "mailboxId": b.aliceMailbox}, nil); got != 422 {
		t.Errorf("creating an app password for another tenant's mailbox returned %d, want 422", got)
	}
	if got := orgAdmin.do("POST", "/organizations", map[string]string{"name": "x", "slug": "x-" + randomSuffix()}, nil); got != 403 {
		t.Errorf("org admin created an organization: %d", got)
	}
	if got := orgAdmin.do("GET", "/admin/identity-providers", nil, nil); got != 403 {
		t.Errorf("org admin listed identity providers: %d", got)
	}

	member := b.bob
	if got := member.do("GET", "/organizations/"+b.orgID+"/mailboxes", nil, nil); got != 403 {
		t.Errorf("ordinary member listed organization mailboxes: %d", got)
	}
	if got := member.do("GET", "/mailboxes/"+b.aliceMailbox+"/folders", nil, nil); got != 404 {
		t.Errorf("member opened a colleague's mailbox: %d", got)
	}
	if got := a.admin.do("GET", "/mailboxes/"+b.bobMailbox+"/messages", nil, nil); got != 404 {
		t.Errorf("platform admin read a user's mail through the API: %d", got)
	}
}

func TestCSRFAndSessionRequired(t *testing.T) {
	anon := newClient(t)
	if got := anon.do("GET", "/session", nil, nil); got != 401 {
		t.Fatalf("anonymous session returned %d", got)
	}
	c := adminClient(t)
	c.csrf = ""
	if got := c.do("POST", "/organizations", map[string]string{"name": "x", "slug": "csrf-" + randomSuffix()}, nil); got != 403 {
		t.Fatalf("state change without CSRF token returned %d", got)
	}
}

func TestLoginRateLimit(t *testing.T) {
	c := newClient(t)
	email := "nobody-" + randomSuffix() + "@example.test"
	codes := map[int]int{}
	for range 12 {
		codes[c.do("POST", "/auth/login", map[string]string{"email": email, "password": "wrong-password"}, nil)]++
	}
	if codes[429] < 2 || codes[401]+codes[429] != 12 {
		t.Fatalf("expected the per-user limit of 10 to rate limit the last attempts, got %v", codes)
	}
}

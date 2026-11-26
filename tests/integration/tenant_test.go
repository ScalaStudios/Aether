//go:build integration

package integration

import (
	"strings"
	"sync"
	"testing"
)

func TestTenantIsolation(t *testing.T) {
	a := setupWorld(t)
	b := setupWorld(t)
	a.admin.must("POST", "/organizations/"+a.orgID+"/members", map[string]string{"email": "alice@" + a.domain, "role": "admin"}, nil, 204)
	orgAdmin := newClient(t)
	orgAdmin.login("alice@"+a.domain, "alice-integration-pw")

	for _, request := range []struct{ method, path string }{
		{"GET", "/admin/outbound-routing"},
		{"PUT", "/admin/outbound-routing/relay/" + a.domain},
		{"DELETE", "/admin/outbound-routing/relay/" + a.domain},
	} {
		orgAdmin.must(request.method, request.path, nil, nil, 403)
	}

	var orgs []idOnly
	orgAdmin.must("GET", "/organizations", nil, &orgs, 200)
	if len(orgs) != 1 || orgs[0].ID != a.orgID {
		t.Fatalf("org admin sees organizations %v, want only %s", orgs, a.orgID)
	}
	orgAdmin.must("GET", "/organizations/"+a.orgID+"/mailboxes", nil, nil, 200)
	var bPolicy idOnly
	b.admin.must("POST", "/organizations/"+b.orgID+"/retention-policies", map[string]any{"action": "keep", "days": 10}, &bPolicy, 201)
	var bContact idOnly
	outside := "partner-" + randomSuffix() + "@partner.example"
	b.admin.must("POST", "/organizations/"+b.orgID+"/contacts", map[string]string{"name": "Partner", "email": outside}, &bContact, 201)
	bBob := memberID(t, b, "bob@"+b.domain)

	denied := []struct{ method, path string }{
		{"GET", "/organizations/" + b.orgID},
		{"PATCH", "/organizations/" + b.orgID},
		{"GET", "/organizations/" + b.orgID + "/domains"},
		{"GET", "/organizations/" + b.orgID + "/personal-alias-policy"},
		{"PUT", "/organizations/" + b.orgID + "/personal-alias-policy"},
		{"GET", "/organizations/" + b.orgID + "/personal-aliases"},
		{"POST", "/organizations/" + b.orgID + "/personal-aliases"},
		{"PUT", "/organizations/" + b.orgID + "/personal-alias-key"},
		{"DELETE", "/organizations/" + b.orgID + "/personal-alias-key"},
		{"GET", "/organizations/" + b.orgID + "/mailboxes"},
		{"GET", "/organizations/" + b.orgID + "/import-schedules"},
		{"POST", "/organizations/" + b.orgID + "/import-schedules"},
		{"POST", "/organizations/" + b.orgID + "/import-schedules/stop"},
		{"GET", "/organizations/" + b.orgID + "/import-schedules/" + b.bobMailbox + "/runs"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/import-schedules"},
		{"POST", "/mailboxes/" + b.bobMailbox + "/import-schedules"},
		{"DELETE", "/mailboxes/" + b.bobMailbox + "/import-schedules/" + a.bobMailbox},
		{"GET", "/organizations/" + b.orgID + "/members"},
		{"GET", "/organizations/" + b.orgID + "/audit"},
		{"GET", "/organizations/" + b.orgID + "/forwarding"},
		{"PUT", "/organizations/" + b.orgID + "/forwarding"},
		{"GET", "/organizations/" + b.orgID + "/message-trace"},
		{"GET", "/organizations/" + b.orgID + "/message-trace/export"},
		{"GET", "/organizations/" + b.orgID + "/message-trace/1"},
		{"GET", "/organizations/" + b.orgID + "/mailbox-audit"},
		{"PUT", "/organizations/" + b.orgID + "/mailbox-audit"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/access-log"},
		{"GET", "/organizations/" + b.orgID + "/security"},
		{"PUT", "/organizations/" + b.orgID + "/security"},
		{"GET", "/organizations/" + b.orgID + "/members/" + bBob + "/sessions"},
		{"DELETE", "/organizations/" + b.orgID + "/members/" + bBob + "/sessions"},
		{"DELETE", "/organizations/" + b.orgID + "/members/" + bBob + "/sessions/" + bBob},
		{"GET", "/organizations/" + a.orgID + "/members/" + bBob + "/sessions"},
		{"GET", "/organizations/" + b.orgID + "/audit/export"},
		{"GET", "/organizations/" + b.orgID + "/audit/retention"},
		{"PUT", "/organizations/" + b.orgID + "/audit/retention"},
		{"GET", "/organizations/" + b.orgID + "/limits"},
		{"GET", "/organizations/" + b.orgID + "/defaults"},
		{"PUT", "/organizations/" + b.orgID + "/defaults"},
		{"GET", "/domains/" + b.domainID + "/defaults"},
		{"PUT", "/domains/" + b.domainID + "/defaults"},
		{"GET", "/organizations/" + b.orgID + "/features"},
		{"PUT", "/organizations/" + b.orgID + "/features"},
		{"GET", "/domains/" + b.domainID},
		{"GET", "/domains/" + b.domainID + "/dns"},
		{"POST", "/domains/" + b.domainID + "/verify"},
		{"PUT", "/domains/" + b.domainID + "/coexistence"},
		{"GET", "/domains/" + b.domainID + "/dkim"},
		{"DELETE", "/domains/" + b.domainID},
		{"PATCH", "/domains/" + b.domainID},
		{"PATCH", "/mailboxes/" + b.bobMailbox},
		{"DELETE", "/mailboxes/" + b.bobMailbox},
		{"POST", "/mailboxes/" + b.bobMailbox + "/restore"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/exports"},
		{"POST", "/mailboxes/" + b.bobMailbox + "/exports"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/exports/" + b.bobMailbox + "/download"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/folders"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/messages"},
		{"POST", "/mailboxes/" + b.bobMailbox + "/send"},
		{"PUT", "/mailboxes/" + b.bobMailbox + "/drafts"},
		{"DELETE", "/mailboxes/" + b.bobMailbox + "/drafts/1"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/rules"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/auto-reply"},
		{"GET", "/mailboxes/" + b.bobMailbox + "/retention"},
		{"GET", "/organizations/" + b.orgID + "/retention-policies"},
		{"POST", "/organizations/" + b.orgID + "/retention-policies"},
		{"PATCH", "/retention-policies/" + bPolicy.ID},
		{"DELETE", "/retention-policies/" + bPolicy.ID},
		{"GET", "/mailboxes/" + b.bobMailbox + "/recoverable"},
		{"POST", "/mailboxes/" + b.bobMailbox + "/recoverable/restore"},
		{"GET", "/organizations/" + b.orgID + "/contacts"},
		{"POST", "/organizations/" + b.orgID + "/contacts"},
		{"PUT", "/organizations/" + b.orgID + "/contacts/" + bContact.ID},
		{"DELETE", "/organizations/" + b.orgID + "/contacts/" + bContact.ID},
		{"GET", "/mailboxes/" + b.bobMailbox + "/tagged-mail"},
		{"PUT", "/mailboxes/" + b.bobMailbox + "/tagged-mail"},
	}
	for _, d := range denied {
		var body any
		if d.method == "PUT" {
			body = map[string]any{"subject": "x"}
		}
		if d.path == "/organizations/"+b.orgID+"/mailbox-audit" && d.method == "PUT" {
			body = map[string]any{"enabled": true, "retentionDays": 30}
		}
		if d.method == "PATCH" {
			body = map[string]bool{"active": false}
		}
		if d.method == "PATCH" && strings.HasPrefix(d.path, "/organizations/") {
			body = map[string][]string{"mailboxProtocols": {}}
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
	if got := orgAdmin.do("POST", "/organizations/"+a.orgID+"/retention-policies", map[string]any{"action": "keep", "days": 10, "mailboxId": b.bobMailbox}, nil); got != 422 {
		t.Errorf("creating a retention policy for another tenant's mailbox returned %d, want 422", got)
	}
	if got := orgAdmin.do("POST", "/organizations/"+a.orgID+"/domains", map[string]any{"name": "steal" + randomSuffix() + ".test", "aliasOf": b.domainID}, nil); got != 422 {
		t.Errorf("creating an alias domain of another tenant's domain returned %d, want 422", got)
	}
	foreign := "alice@" + b.domain
	orgAdmin.must("POST", "/mailboxes/"+a.aliceMailbox+"/send", map[string]string{"fromAddress": foreign}, nil, 403)
	orgAdmin.must("PUT", "/mailboxes/"+a.aliceMailbox+"/signatures?fromAddress="+foreign, map[string]string{"displayName": "Foreign"}, nil, 403)
	orgAdmin.must("POST", "/organizations/"+a.orgID+"/aliases", map[string]any{"domainId": a.domainID, "localPart": "team", "kind": "list", "destinations": []string{"alice@" + a.domain}, "sentMailboxId": createSharedMailbox(t, b).ID}, nil, 422)
	if got := orgAdmin.do("POST", "/me/app-passwords", map[string]string{"name": "steal", "mailboxId": b.aliceMailbox}, nil); got != 422 {
		t.Errorf("creating an app password for another tenant's mailbox returned %d, want 422", got)
	}
	var seen []struct {
		Address string `json:"address"`
	}
	orgAdmin.must("GET", "/directory?q=partner", nil, &seen, 200)
	if len(seen) != 0 {
		t.Errorf("another tenant's organization contacts are in the directory: %v", seen)
	}
	if got := orgAdmin.do("PUT", "/organizations/"+a.orgID+"/contacts/"+bContact.ID, map[string]string{"name": "Moved", "email": outside}, nil); got != 404 {
		t.Errorf("updating another tenant's contact through one's own organization returned %d, want 404", got)
	}
	if got := orgAdmin.do("POST", "/organizations", map[string]string{"name": "x", "slug": "x-" + randomSuffix()}, nil); got != 403 {
		t.Errorf("org admin created an organization: %d", got)
	}
	if got := orgAdmin.do("PUT", "/organizations/"+a.orgID+"/limits", map[string]int64{"maxMailboxes": 1000}, nil); got != 403 {
		t.Errorf("org admin raised its own limits: %d", got)
	}
	if got := orgAdmin.do("GET", "/admin/identity-providers", nil, nil); got != 403 {
		t.Errorf("org admin listed identity providers: %d", got)
	}
	if got := orgAdmin.do("GET", "/admin/message-trace", nil, nil); got != 403 {
		t.Errorf("org admin searched the whole server's message trace: %d", got)
	}
	if got := orgAdmin.do("GET", "/admin/mail-queue", nil, nil); got != 403 {
		t.Errorf("org admin listed the mail queue: %d", got)
	}
	if got := orgAdmin.do("POST", "/admin/mail-queue/delete", map[string]any{"filter": map[string]string{"queue": "deferred"}}, nil); got != 403 {
		t.Errorf("org admin deleted queued mail: %d", got)
	}
	if got := orgAdmin.do("PUT", "/admin/users/"+bBob+"/password", map[string]string{"password": "taken-over-password"}, nil); got != 403 {
		t.Errorf("org admin reset another tenant's password: %d", got)
	}
	if got := orgAdmin.do("DELETE", "/admin/users/"+bBob+"/two-factor", nil, nil); got != 403 {
		t.Errorf("org admin reset another tenant's two-factor: %d", got)
	}

	policyPath := "/organizations/" + a.orgID + "/personal-alias-policy"
	orgAdmin.must("PUT", policyPath, map[string]any{"limitPerUser": 3, "domainIds": []string{b.domainID}}, nil, 422)
	orgAdmin.must("PUT", policyPath, map[string]any{"limitPerUser": 3, "domainIds": []string{a.domainID}}, nil, 204)
	for _, target := range []map[string]string{{"mailboxId": b.bobMailbox, "domainId": a.domainID}, {"mailboxId": a.aliceMailbox, "domainId": b.domainID}} {
		orgAdmin.must("POST", "/organizations/"+a.orgID+"/personal-aliases", target, nil, 403)
		orgAdmin.must("PUT", "/organizations/"+a.orgID+"/personal-alias-key", target, nil, 403)
	}

	aImports, bImports := "/organizations/"+a.orgID+"/import-schedules", "/organizations/"+b.orgID+"/import-schedules"
	input := func(mailbox string) map[string]any {
		return map[string]any{"mailboxId": mailbox, "host": "imap.example.test", "security": "tls", "username": "source", "password": "secret", "intervalMinutes": 60}
	}
	orgAdmin.must("POST", aImports, map[string]any{"imports": []any{input(a.bobMailbox), input(b.bobMailbox)}}, nil, 404)
	var jobs []idOnly
	orgAdmin.must("GET", aImports, nil, &jobs, 200)
	if len(jobs) != 0 {
		t.Fatal("cross-tenant batch partially created schedules")
	}
	b.admin.must("POST", bImports, map[string]any{"imports": []any{input(b.bobMailbox)}}, &jobs, 202)
	orgAdmin.must("GET", aImports+"/"+jobs[0].ID+"/runs", nil, nil, 404)
	orgAdmin.must("POST", aImports+"/stop", map[string]any{"ids": []string{jobs[0].ID}}, nil, 404)
	b.admin.must("POST", bImports+"/stop", map[string]any{"ids": []string{jobs[0].ID}}, nil, 204)
	b.bob.must("GET", bImports, nil, nil, 403)
	for _, path := range []string{"/admin/audit", "/admin/audit/export", "/admin/webhooks"} {
		if got := orgAdmin.do("GET", path, nil, nil); got != 403 {
			t.Errorf("org admin GET %s returned %d, want 403", path, got)
		}
	}
	for _, call := range []struct{ method, path string }{
		{"GET", "/admin/alerts/rules"},
		{"PATCH", "/admin/alerts/rules/mail_queue_size"},
		{"GET", "/admin/alerts"},
		{"POST", "/admin/alerts/test"},
		{"GET", "/admin/alerts/00000000-0000-0000-0000-000000000000"},
		{"POST", "/admin/alerts/00000000-0000-0000-0000-000000000000/acknowledge"},
		{"POST", "/admin/alerts/00000000-0000-0000-0000-000000000000/resolve"},
	} {
		if got := orgAdmin.do(call.method, call.path, map[string]any{}, nil); got != 403 {
			t.Errorf("org admin called %s %s: %d", call.method, call.path, got)
		}
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

func TestOrganizationKeepsAnOwner(t *testing.T) {
	w := setupWorld(t)
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	members := "/organizations/" + w.orgID + "/members"
	owners := func() int {
		var list []struct {
			Role string `json:"role"`
		}
		w.admin.must("GET", members, nil, &list, 200)
		n := 0
		for _, m := range list {
			if m.Role == "owner" {
				n++
			}
		}
		return n
	}
	w.admin.must("POST", members, map[string]string{"email": alice, "role": "owner"}, nil, 204)
	if code, reason := w.alice.failure("POST", members, map[string]string{"email": alice, "role": "member"}); code != 409 || reason != "last_owner" {
		t.Fatalf("sole owner demoted themselves: %d %s", code, reason)
	}
	w.admin.must("POST", members, map[string]string{"email": bob, "role": "owner"}, nil, 204)
	w.alice.must("POST", members, map[string]string{"email": alice, "role": "admin"}, nil, 204)
	w.admin.must("POST", members, map[string]string{"email": alice, "role": "owner"}, nil, 204)

	ids := map[string]string{alice: memberID(t, w, alice), bob: memberID(t, w, bob)}
	for i := range 10 {
		var wg sync.WaitGroup
		for email, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if i%2 == 0 {
					w.admin.do("POST", members, map[string]string{"email": email, "role": "member"}, nil)
				} else {
					w.admin.do("DELETE", members+"/"+id, nil, nil)
				}
			}()
		}
		wg.Wait()
		if n := owners(); n != 1 {
			t.Fatalf("round %d left %d owners", i, n)
		}
		w.admin.must("POST", members, map[string]string{"email": alice, "role": "owner"}, nil, 204)
		w.admin.must("POST", members, map[string]string{"email": bob, "role": "owner"}, nil, 204)
	}
}

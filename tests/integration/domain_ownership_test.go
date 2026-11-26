//go:build integration

package integration

import (
	"context"
	"testing"
)

func orgAdminOf(t *testing.T, w *world) *apiClient {
	t.Helper()
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": "alice@" + w.domain, "role": "admin"}, nil, 204)
	c := newClient(t)
	c.login("alice@"+w.domain, "alice-integration-pw")
	return c
}

func TestMailboxesNeedAVerifiedDomainAndKeepExistingSignIns(t *testing.T) {
	w := setupWorld(t)
	orgAdmin := orgAdminOf(t, w)
	db := testDB(t)
	ctx := context.Background()

	unverified := "unverified" + randomSuffix() + ".test"
	var dom struct {
		ID       string `json:"id"`
		Verified bool   `json:"verified"`
	}
	orgAdmin.must("POST", "/organizations/"+w.orgID+"/domains", map[string]string{"name": unverified}, &dom, 201)
	if dom.Verified {
		t.Fatal("a domain added by an org admin should start unverified")
	}
	if code := errorCode(t, orgAdmin, "POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": dom.ID, "localPart": "ceo", "password": "chosen-password-123"}, 422); code != "invalid_domain" {
		t.Fatalf("mailbox on an unverified domain returned %q, want invalid_domain", code)
	}
	for _, dryRun := range []bool{true, false} {
		var res bulkResponseBody
		orgAdmin.must("POST", "/organizations/"+w.orgID+"/bulk", map[string]any{"dryRun": dryRun, "operations": []map[string]any{{"type": "mailbox", "address": "newhire@" + unverified, "password": "chosen-password-123"}}}, &res, 200)
		if len(res.Results) != 1 || res.Results[0].Status != "failed" || res.Results[0].Error == nil || res.Results[0].Error.Code != "invalid_domain" {
			t.Fatalf("bulk mailbox on an unverified domain (dryRun %v) = %+v", dryRun, res.Results)
		}
	}
	var users int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM users WHERE email IN ($1, $2)`, "ceo@"+unverified, "newhire@"+unverified).Scan(&users); err != nil || users != 0 {
		t.Fatalf("accounts created on an unverified domain: %d (%v)", users, err)
	}

	existing := "carol@" + w.domain
	if _, err := db.Exec(ctx, `INSERT INTO users (email, display_name) VALUES ($1, 'Carol')`, existing); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*apiClient{orgAdmin, w.admin} {
		if code := errorCode(t, c, "POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "carol", "password": "chosen-password-123"}, 409); code != "account_exists" {
			t.Fatalf("mailbox with a password for an existing account returned %q, want account_exists", code)
		}
		var res bulkResponseBody
		c.must("POST", "/organizations/"+w.orgID+"/bulk", map[string]any{"dryRun": true, "operations": []map[string]any{{"type": "mailbox", "address": existing, "password": "chosen-password-123"}}}, &res, 200)
		if len(res.Results) != 1 || res.Results[0].Error == nil || res.Results[0].Error.Code != "account_exists" {
			t.Fatalf("bulk dry run with a password for an existing account = %+v", res.Results)
		}
	}
	var hasPassword bool
	if err := db.QueryRow(ctx, `SELECT password_hash IS NOT NULL FROM users WHERE email = $1`, existing).Scan(&hasPassword); err != nil || hasPassword {
		t.Fatalf("existing account got a password: %v (%v)", hasPassword, err)
	}
	orgAdmin.must("POST", "/organizations/"+w.orgID+"/mailboxes", map[string]any{"domainId": w.domainID, "localPart": "carol"}, nil, 201)
}

func TestUnverifiedDomainDoesNotBlockAProvenClaim(t *testing.T) {
	a := setupWorld(t)
	b := setupWorld(t)
	bAdmin := orgAdminOf(t, b)
	name := "claimed" + randomSuffix() + ".test"
	var held idOnly
	bAdmin.must("POST", "/organizations/"+b.orgID+"/domains", map[string]string{"name": name}, &held, 201)
	if code := errorCode(t, bAdmin, "POST", "/organizations/"+b.orgID+"/domains", map[string]any{"name": "alias" + randomSuffix() + ".test", "aliasOf": held.ID}, 422); code != "invalid_alias_target" {
		t.Fatalf("alias domain of an unverified domain returned %q, want invalid_alias_target", code)
	}
	for _, c := range []*apiClient{orgAdminOf(t, a), a.admin} {
		if code := errorCode(t, c, "POST", "/organizations/"+a.orgID+"/domains", map[string]string{"name": name}, 409); code != "domain_claim_required" {
			t.Fatalf("adding a domain another org holds unverified returned %q, want domain_claim_required", code)
		}
	}
	bAdmin.must("GET", "/domains/"+held.ID, nil, nil, 200)
	bAdmin.must("POST", "/domains/"+held.ID+"/verify", nil, nil, 422)
	if code := errorCode(t, bAdmin, "POST", "/organizations/"+b.orgID+"/domains", map[string]string{"name": name}, 409); code != "domain_taken" {
		t.Fatalf("adding the same domain twice in one org returned %q, want domain_taken", code)
	}
	if code := errorCode(t, a.admin, "POST", "/organizations/"+a.orgID+"/domains", map[string]string{"name": b.domain}, 409); code != "domain_taken" {
		t.Fatalf("adding another org's verified domain returned %q, want domain_taken", code)
	}
}

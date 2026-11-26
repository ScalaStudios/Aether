//go:build integration

package integration

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
)

type scimUser struct {
	ID       string `json:"id"`
	UserName string `json:"userName"`
	Active   bool   `json:"active"`
}

func TestSCIMProvisioning(t *testing.T) {
	w := setupWorld(t)
	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider struct {
		ID string `json:"id"`
	}
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)

	var enabled struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	w.admin.must("POST", "/admin/identity-providers/"+provider.ID+"/scim-token", nil, &enabled, 200)

	anonymous := newClient(t)
	if got := anonymous.do("GET", "/scim/"+slug+"/ServiceProviderConfig", nil, nil); got != 401 {
		t.Fatalf("SCIM without a token: %d", got)
	}
	halo := newClient(t)
	halo.bearer = enabled.Token
	halo.must("GET", "/scim/"+slug+"/ServiceProviderConfig", nil, nil, 200)

	email, subject := "erin@"+w.domain, "halo-"+randomSuffix()
	body := map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": email, "externalId": subject,
		"displayName": "Erin Example", "emails": []map[string]any{{"value": email, "type": "work", "primary": true}}, "active": true,
	}
	var created scimUser
	halo.must("POST", "/scim/"+slug+"/Users", body, &created, 201)
	if created.ID == "" || created.UserName != email || !created.Active {
		t.Fatalf("created %+v", created)
	}
	if got := halo.do("POST", "/scim/"+slug+"/Users", body, nil); got != 409 {
		t.Fatalf("duplicate create: %d, want 409", got)
	}
	var list struct {
		Resources []scimUser `json:"Resources"`
	}
	halo.must("GET", "/scim/"+slug+"/Users?filter="+url.QueryEscape(`userName eq "`+email+`"`), nil, &list, 200)
	if len(list.Resources) != 1 || list.Resources[0].ID != created.ID {
		t.Fatalf("filter returned %+v", list)
	}

	var mailboxes []struct {
		Address string `json:"address"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/mailboxes", nil, &mailboxes, 200)
	found := false
	for _, m := range mailboxes {
		found = found || m.Address == email
	}
	if !found {
		t.Fatal("SCIM create did not provision the mailbox")
	}

	f.subject, f.email = subject, email
	oidcLogin(t, f, slug)

	deactivate := map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": false}},
	}
	var patched scimUser
	halo.must("PATCH", "/scim/"+slug+"/Users/"+created.ID, deactivate, &patched, 200)
	if patched.Active {
		t.Fatal("PATCH active=false left the user active")
	}
	var restored scimUser
	halo.must("PUT", "/scim/"+slug+"/Users/"+created.ID, body, &restored, 200)
	if !restored.Active {
		t.Fatal("PUT active=true did not restore a user that SCIM suspended")
	}

	w.admin.must("PATCH", "/admin/users/"+created.ID, map[string]string{"status": "suspended"}, nil, 204)
	halo.must("PUT", "/scim/"+slug+"/Users/"+created.ID, body, &restored, 200)
	if restored.Active {
		t.Fatal("SCIM restored a user that an administrator suspended")
	}

	if got := halo.do("DELETE", "/scim/"+slug+"/Users/"+created.ID, nil, nil); got != 204 {
		t.Fatalf("delete: %d", got)
	}
	w.admin.must("DELETE", "/admin/identity-providers/"+provider.ID+"/scim-token", nil, nil, 204)
	if got := halo.do("GET", "/scim/"+slug+"/Users/"+created.ID, nil, nil); got != 401 {
		t.Fatalf("SCIM after the token was removed: %d", got)
	}
}

func TestSCIMSkipsPersonalAliasAddress(t *testing.T) {
	w := setupWorld(t)
	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider struct {
		ID string `json:"id"`
	}
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret,
	}, &provider, 201)
	defer w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil)
	var enabled struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/admin/identity-providers/"+provider.ID+"/scim-token", nil, &enabled, 200)

	base := "/organizations/" + w.orgID
	w.admin.must("PUT", base+"/personal-alias-policy", map[string]any{"limitPerUser": 1, "domainIds": []string{w.domainID}}, nil, 204)
	email := "newhire" + randomSuffix() + "@" + w.domain
	w.alice.must("POST", base+"/personal-aliases", map[string]any{"mailboxId": w.aliceMailbox, "domainId": w.domainID, "localPart": email[:len(email)-len(w.domain)-1]}, nil, 201)

	halo := newClient(t)
	halo.bearer = enabled.Token
	halo.must("POST", "/scim/"+slug+"/Users", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": email, "externalId": "halo-" + randomSuffix(),
		"emails": []map[string]any{{"value": email, "primary": true}}, "active": true,
	}, nil, 201)
	var mailboxes []struct {
		Address string `json:"address"`
	}
	w.admin.must("GET", base+"/mailboxes", nil, &mailboxes, 200)
	for _, m := range mailboxes {
		if m.Address == email {
			t.Fatal("provisioning created a mailbox behind a personal alias")
		}
	}
}

func scimClient(t *testing.T, w *world, f *fakeIssuer, autoProvision bool) (string, string, *apiClient) {
	t.Helper()
	slug := "halo-" + randomSuffix()
	var provider idOnly
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret, "autoProvision": autoProvision,
	}, &provider, 201)
	t.Cleanup(func() { w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil) })
	var enabled struct {
		Token string `json:"token"`
	}
	w.admin.must("POST", "/admin/identity-providers/"+provider.ID+"/scim-token", nil, &enabled, 200)
	halo := newClient(t)
	halo.bearer = enabled.Token
	return slug, provider.ID, halo
}

func scimUserBody(email, externalID string, active bool) map[string]any {
	return map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": email, "externalId": externalID, "active": active}
}

func scimActive(active bool) map[string]any {
	return map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []map[string]any{{"op": "replace", "path": "active", "value": active}}}
}

func TestSCIMOnlyReachesLinkedAccounts(t *testing.T) {
	w := setupWorld(t)
	db := testDB(t)
	ctx := context.Background()
	slug, providerID, halo := scimClient(t, w, startFakeIssuer(t), false)
	base := "/scim/" + slug + "/Users/"

	adminID, aliceID := userID(t, w.admin), userID(t, w.alice)
	var adminEmail string
	if err := db.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, adminID).Scan(&adminEmail); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ id, email string }{{adminID, adminEmail}, {aliceID, "alice@" + w.domain}} {
		var list struct {
			Resources []scimUser `json:"Resources"`
		}
		halo.must("GET", "/scim/"+slug+"/Users?filter="+url.QueryEscape(`userName eq "`+target.email+`"`), nil, &list, 200)
		if len(list.Resources) != 0 {
			t.Fatalf("filter found an account this provider is not linked to: %+v", list.Resources)
		}
		for _, call := range []struct {
			method string
			body   any
		}{{"GET", nil}, {"PUT", scimUserBody(target.email, "other-"+randomSuffix(), true)}, {"PATCH", scimActive(false)}, {"DELETE", nil}} {
			if got := halo.do(call.method, base+target.id, call.body, nil); got != 404 {
				t.Fatalf("%s on an unlinked account returned %d, want 404", call.method, got)
			}
		}
		var status string
		var links int
		if err := db.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM user_identities WHERE user_id = $1 AND provider_id = $2) FROM users WHERE id = $1`, target.id, providerID).Scan(&status, &links); err != nil {
			t.Fatal(err)
		}
		if status != "active" || links != 0 {
			t.Fatalf("unlinked account changed: status %s, links %d", status, links)
		}
	}

	if got := halo.do("POST", "/scim/"+slug+"/Users", scimUserBody("nolink"+randomSuffix()+"@"+w.domain, "", true), nil); got != 400 {
		t.Fatalf("create without externalId returned %d, want 400", got)
	}

	first, subject := "first"+randomSuffix()+"@"+w.domain, "halo-"+randomSuffix()
	var created scimUser
	halo.must("POST", "/scim/"+slug+"/Users", scimUserBody(first, subject, true), &created, 201)
	second := "second" + randomSuffix() + "@" + w.domain
	if got := halo.do("POST", "/scim/"+slug+"/Users", scimUserBody(second, subject, true), nil); got != 409 {
		t.Fatalf("create with an externalId linked to another user returned %d, want 409", got)
	}
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)`, second).Scan(&exists); err != nil || exists {
		t.Fatalf("conflicting create left an account behind: %v %v", exists, err)
	}

	moved := "halo-" + randomSuffix()
	halo.must("PUT", base+created.ID, scimUserBody(first, moved, true), nil, 200)
	rows, err := db.Query(ctx, `SELECT subject FROM user_identities WHERE provider_id = $1 AND user_id = $2`, providerID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(subjects) != 1 || subjects[0] != moved {
		t.Fatalf("identities after externalId change: %v %v", subjects, err)
	}

	if _, err := db.Exec(ctx, `UPDATE users SET is_platform_admin = true WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if got := halo.do("GET", base+created.ID, nil, nil); got != 404 {
		t.Fatalf("GET on a linked platform admin returned %d, want 404", got)
	}
	var list struct {
		Resources []scimUser `json:"Resources"`
	}
	halo.must("GET", "/scim/"+slug+"/Users?filter="+url.QueryEscape(`userName eq "`+first+`"`), nil, &list, 200)
	if len(list.Resources) != 0 {
		t.Fatalf("filter found a linked platform admin: %+v", list.Resources)
	}
}

//go:build integration

package integration

import (
	"net/url"
	"testing"
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

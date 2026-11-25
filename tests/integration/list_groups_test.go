//go:build integration

package integration

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

type scimGroupBody struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Members     []struct {
		Value string `json:"value"`
	} `json:"members"`
}

type directoryGroupBody struct {
	ID      string `json:"id"`
	Members int    `json:"members"`
}

type resolvedMember struct {
	Address string   `json:"address"`
	Direct  bool     `json:"direct"`
	Domain  bool     `json:"domain"`
	Groups  []string `json:"groups"`
}

func TestListsFromGroupsAndRules(t *testing.T) {
	w := setupWorld(t)
	submissionAddr := env(t, "AETHER_TEST_SUBMISSION", "postfix:587")
	alice, bob := "alice@"+w.domain, "bob@"+w.domain
	aliceID, bobID := userID(t, w.alice), userID(t, w.bob)

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
	halo := newClient(t)
	halo.bearer = enabled.Token

	name := "Sales " + randomSuffix()
	var group scimGroupBody
	halo.must("POST", "/scim/"+slug+"/Groups", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": name, "members": []map[string]string{{"value": aliceID}},
	}, &group, 201)
	if group.ID == "" || len(group.Members) != 1 || group.Members[0].Value != aliceID {
		t.Fatalf("created group %+v", group)
	}
	if code := halo.do("POST", "/scim/"+slug+"/Groups", map[string]any{"displayName": name}, nil); code != 409 {
		t.Fatalf("duplicate group: %d, want 409", code)
	}
	if code := halo.do("POST", "/scim/"+slug+"/Groups", map[string]any{"displayName": "Ghosts " + randomSuffix(), "members": []map[string]string{{"value": "00000000-0000-0000-0000-000000000000"}}}, nil); code != 400 {
		t.Fatalf("group with an unknown member: %d, want 400", code)
	}
	var found struct {
		Resources []scimGroupBody `json:"Resources"`
	}
	halo.must("GET", "/scim/"+slug+"/Groups?excludedAttributes=members&filter="+url.QueryEscape(`displayName eq "`+name+`"`), nil, &found, 200)
	if len(found.Resources) != 1 || found.Resources[0].ID != group.ID || len(found.Resources[0].Members) != 0 {
		t.Fatalf("filter returned %+v", found)
	}

	var groups []directoryGroupBody
	w.admin.must("GET", "/organizations/"+w.orgID+"/directory-groups", nil, &groups, 200)
	if !slices.ContainsFunc(groups, func(g directoryGroupBody) bool { return g.ID == group.ID && g.Members == 1 }) {
		t.Fatalf("directory groups %+v", groups)
	}

	if code := w.admin.do("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "empty", "kind": "list"}, nil); code != 422 {
		t.Fatalf("list without members: %d, want 422", code)
	}
	if code := w.admin.do("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{"domainId": w.domainID, "localPart": "fwd", "groups": []string{group.ID}}, nil); code != 422 {
		t.Fatalf("group on a plain alias: %d, want 422", code)
	}
	var list aliasBody
	w.admin.must("POST", "/organizations/"+w.orgID+"/aliases", map[string]any{
		"domainId": w.domainID, "localPart": "sales", "kind": "list", "groups": []string{group.ID}, "acceptFrom": "members",
	}, &list, 201)

	members := func() []resolvedMember {
		var out []resolvedMember
		w.admin.must("GET", "/aliases/"+list.ID+"/members", nil, &out, 200)
		return out
	}
	if got := members(); len(got) != 1 || got[0].Address != alice || got[0].Direct || len(got[0].Groups) != 1 || got[0].Groups[0] != name {
		t.Fatalf("resolved members %+v", got)
	}
	if code := w.bob.do("GET", "/aliases/"+list.ID+"/members", nil, nil); code != 403 && code != 404 {
		t.Fatalf("a member read the resolved members: %d", code)
	}

	var ap struct {
		Password string `json:"password"`
	}
	w.bob.must("POST", "/me/app-passwords", map[string]string{"name": "integration", "mailboxId": w.bobMailbox}, &ap, 201)
	err := submit(t, submissionAddr, bob, ap.Password, bob, []string{list.Address}, rfc822(bob, list.Address, "early", "x"), false)
	if err == nil || !strings.Contains(err.Error(), "does not accept mail") {
		t.Fatalf("non-member sending to a members-only list: %v", err)
	}

	halo.must("PATCH", "/scim/"+slug+"/Groups/"+group.ID, map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "Add", "path": "members", "value": []map[string]string{{"value": bobID}}}},
	}, &group, 200)
	if len(group.Members) != 2 {
		t.Fatalf("after adding bob %+v", group)
	}
	subject := "group list " + randomSuffix()
	if err := submit(t, submissionAddr, bob, ap.Password, bob, []string{list.Address}, rfc822(bob, list.Address, subject, "x"), false); err != nil {
		t.Fatalf("group member sending to the list: %v", err)
	}
	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	waitFor(t, "group list delivery", func() bool { return imapFindSubject(t, c, "INBOX", subject) != nil })

	halo.must("PATCH", "/scim/"+slug+"/Groups/"+group.ID, map[string]any{
		"Operations": []map[string]any{{"op": "remove", "path": `members[value eq "` + aliceID + `"]`}},
	}, &group, 200)
	if got := members(); len(got) != 1 || got[0].Address != bob {
		t.Fatalf("after removing alice %+v", got)
	}

	var updated aliasBody
	w.admin.must("PATCH", "/aliases/"+list.ID, map[string]any{"groups": []string{}, "memberDomains": []string{w.domainID}, "destinations": []string{"partner@external.example"}}, &updated, 200)
	got := members()
	addresses := []string{}
	for _, m := range got {
		addresses = append(addresses, m.Address)
		if m.Address == alice && !m.Domain || len(m.Groups) > 0 {
			t.Fatalf("domain member %+v", m)
		}
	}
	if !slices.Contains(addresses, alice) || !slices.Contains(addresses, bob) || !slices.Contains(addresses, "partner@external.example") {
		t.Fatalf("domain rule members %+v", got)
	}

	w.admin.must("PATCH", "/aliases/"+list.ID, map[string]any{"groups": []string{group.ID}, "memberDomains": []string{}, "destinations": []string{}}, &updated, 200)
	var emptied scimGroupBody
	halo.must("PUT", "/scim/"+slug+"/Groups/"+group.ID, map[string]any{"displayName": name, "members": []map[string]string{}}, &emptied, 200)
	if len(emptied.Members) != 0 || len(members()) != 0 {
		t.Fatalf("PUT without members left %+v", emptied)
	}
	halo.must("DELETE", "/scim/"+slug+"/Groups/"+group.ID, nil, nil, 204)
	if got := members(); len(got) != 0 {
		t.Fatalf("members after the group was deleted %+v", got)
	}
	w.admin.must("DELETE", "/aliases/"+list.ID, nil, nil, 204)
}

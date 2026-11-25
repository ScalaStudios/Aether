//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type limitsView struct {
	MaxMailboxes    int64 `json:"maxMailboxes"`
	MaxStorageBytes int64 `json:"maxStorageBytes"`
	Usage           struct {
		Mailboxes, Aliases, Lists, Domains, StorageBytes int64
		NoQuota                                          int64 `json:"mailboxesWithoutQuota"`
	} `json:"usage"`
}

type quotaDefaultView struct {
	QuotaBytes          *int64 `json:"quotaBytes"`
	EffectiveQuotaBytes int64  `json:"effectiveQuotaBytes"`
}

func TestOrganizationLimitsAndDefaults(t *testing.T) {
	w := setupWorld(t)
	org := "/organizations/" + w.orgID
	setLimits := func(l map[string]int64) {
		t.Helper()
		w.admin.must("PUT", org+"/limits", l, nil, 200)
	}
	mailbox := func(local string, quota any) map[string]any {
		body := map[string]any{"domainId": w.domainID, "localPart": local}
		if quota != nil {
			body["quotaBytes"] = quota
		}
		return body
	}
	var created struct {
		ID         string `json:"id"`
		QuotaBytes int64  `json:"quotaBytes"`
	}

	if got := w.alice.do("GET", org+"/limits", nil, nil); got != 403 {
		t.Fatalf("member read limits: %d", got)
	}
	if got := w.alice.do("PUT", org+"/limits", map[string]int64{"maxMailboxes": 100}, nil); got != 403 {
		t.Fatalf("member set limits: %d", got)
	}
	if code := errorCode(t, w.admin, "PUT", org+"/limits", map[string]int64{"maxAliases": -1}, 422); code != "invalid_limit" {
		t.Fatalf("negative limit: %s", code)
	}

	setLimits(map[string]int64{"maxMailboxes": 3, "maxAliases": 1, "maxLists": 1, "maxDomains": 1})
	var l limitsView
	w.admin.must("GET", org+"/limits", nil, &l, 200)
	if l.MaxMailboxes != 3 || l.Usage.Mailboxes != 2 || l.Usage.Domains != 1 || l.Usage.NoQuota != 2 {
		t.Fatalf("limits %+v", l)
	}

	if code := errorCode(t, w.admin, "POST", org+"/domains", map[string]string{"name": "extra" + randomSuffix() + ".test"}, 409); code != "domain_limit_reached" {
		t.Fatalf("domain over limit: %s", code)
	}
	alias := map[string]any{"domainId": w.domainID, "localPart": "hello", "destinations": []string{"alice@" + w.domain}}
	var first idOnly
	w.admin.must("POST", org+"/aliases", alias, &first, 201)
	alias["localPart"] = "hello2"
	if code := errorCode(t, w.admin, "POST", org+"/aliases", alias, 409); code != "alias_limit_reached" {
		t.Fatalf("alias over limit: %s", code)
	}
	list := map[string]any{"domainId": w.domainID, "localPart": "staff", "kind": "list", "name": "Staff", "destinations": []string{"alice@" + w.domain}}
	w.admin.must("POST", org+"/aliases", list, nil, 201)
	list["localPart"] = "staff2"
	if code := errorCode(t, w.admin, "POST", org+"/aliases", list, 409); code != "list_limit_reached" {
		t.Fatalf("list over limit: %s", code)
	}
	if code := errorCode(t, w.admin, "PATCH", "/aliases/"+first.ID, map[string]string{"kind": "list", "name": "Hello"}, 409); code != "list_limit_reached" {
		t.Fatalf("alias turned into list over limit: %s", code)
	}

	var d quotaDefaultView
	w.admin.must("PUT", org+"/defaults", map[string]any{"quotaBytes": 1000}, &d, 200)
	w.admin.must("PUT", "/domains/"+w.domainID+"/defaults", map[string]any{"quotaBytes": 2000}, &d, 200)
	if d.QuotaBytes == nil || *d.QuotaBytes != 2000 || d.EffectiveQuotaBytes != 2000 {
		t.Fatalf("domain default %+v", d)
	}
	if code := errorCode(t, w.admin, "PUT", org+"/defaults", map[string]any{"quotaBytes": -1}, 422); code != "invalid_quota" {
		t.Fatalf("negative default: %s", code)
	}
	w.admin.must("POST", org+"/mailboxes", mailbox("carol", nil), &created, 201)
	if created.QuotaBytes != 2000 {
		t.Fatalf("new mailbox quota %d, want domain default 2000", created.QuotaBytes)
	}
	carol := created.ID
	if code := errorCode(t, w.admin, "POST", org+"/mailboxes", mailbox("dave", nil), 409); code != "mailbox_limit_reached" {
		t.Fatalf("mailbox over limit: %s", code)
	}
	w.admin.must("PUT", "/domains/"+w.domainID+"/defaults", map[string]any{"quotaBytes": nil}, &d, 200)
	if d.QuotaBytes != nil || d.EffectiveQuotaBytes != 1000 {
		t.Fatalf("cleared domain default %+v", d)
	}

	setLimits(map[string]int64{"maxStorageBytes": 5000})
	if code := errorCode(t, w.admin, "POST", org+"/mailboxes", mailbox("dave", 0), 409); code != "quota_required" {
		t.Fatalf("unlimited quota under storage limit: %s", code)
	}
	if code := errorCode(t, w.admin, "POST", org+"/mailboxes", mailbox("dave", 3001), 409); code != "storage_limit_reached" {
		t.Fatalf("quota over storage limit: %s", code)
	}
	w.admin.must("POST", org+"/mailboxes", mailbox("dave", 3000), &created, 201)
	dave := created.ID
	if code := errorCode(t, w.admin, "PATCH", "/mailboxes/"+dave, map[string]int64{"quotaBytes": 3001}, 409); code != "storage_limit_reached" {
		t.Fatalf("raise over storage limit: %s", code)
	}
	if code := errorCode(t, w.admin, "PATCH", "/mailboxes/"+w.aliceMailbox, map[string]int64{"quotaBytes": 1}, 409); code != "storage_limit_reached" {
		t.Fatalf("giving an unlimited mailbox a quota over the limit: %s", code)
	}
	w.admin.must("PATCH", "/mailboxes/"+dave, map[string]int64{"quotaBytes": 2500}, nil, 200)
	w.admin.must("PATCH", "/mailboxes/"+w.bobMailbox, map[string]any{"displayName": "Bob B"}, nil, 200)

	f := startFakeIssuer(t)
	slug := "halo-" + randomSuffix()
	var provider idOnly
	w.admin.must("POST", "/admin/identity-providers", map[string]any{
		"slug": slug, "name": "Halo", "kind": "halo", "issuer": f.issuer, "clientId": f.clientID, "clientSecret": f.secret, "autoProvision": true,
	}, &provider, 201)
	t.Cleanup(func() { w.admin.do("DELETE", "/admin/identity-providers/"+provider.ID, nil, nil) })
	var token struct{ Token string }
	w.admin.must("POST", "/admin/identity-providers/"+provider.ID+"/scim-token", nil, &token, 200)
	scim := newClient(t)
	scim.bearer = token.Token
	w.admin.must("GET", org+"/limits", nil, &l, 200)
	setLimits(map[string]int64{"maxMailboxes": l.Usage.Mailboxes})

	email := "erin@" + w.domain
	var scimErr struct{ Detail, ScimType string }
	scim.mustRaw("POST", "/scim/"+slug+"/Users", "application/scim+json",
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"`+email+`","externalId":"erin","active":true}`, &scimErr, 400)
	if scimErr.ScimType != "invalidValue" || !strings.Contains(scimErr.Detail, "limit") {
		t.Fatalf("SCIM create over limit: %+v", scimErr)
	}

	f.subject, f.email = "halo-"+randomSuffix(), "frank@"+w.domain
	browser, callback, _ := oidcAuthorize(t, f, slug)
	resp, err := browser.http.Get(callback)
	if err != nil || resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), "error=limit_reached") {
		t.Fatalf("OIDC first login over limit: %v %v", err, resp)
	}

	w.admin.must("DELETE", "/mailboxes/"+carol, nil, nil, 204)
	setLimits(map[string]int64{"maxMailboxes": l.Usage.Mailboxes - 1})
	f.subject, f.email = "halo-"+randomSuffix(), "carol@"+w.domain
	oidcLogin(t, f, slug)
	w.admin.must("GET", org+"/limits", nil, &l, 200)
	if l.Usage.Mailboxes != l.MaxMailboxes {
		t.Fatalf("existing account signing in at the limit changed mailboxes to %d", l.Usage.Mailboxes)
	}
	var found struct{ Resources []struct{ ID string } }
	scim.must("GET", "/scim/"+slug+"/Users?filter="+url.QueryEscape(`userName eq "carol@`+w.domain+`"`), nil, &found, 200)
	if len(found.Resources) != 1 {
		t.Fatalf("SCIM filter for carol: %+v", found)
	}
	var replaced struct{ Active bool }
	scim.mustRaw("PUT", "/scim/"+slug+"/Users/"+found.Resources[0].ID, "application/scim+json",
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"carol@`+w.domain+`","active":false}`, &replaced, 200)
	if replaced.Active {
		t.Fatal("SCIM PUT at the limit did not suspend the account")
	}
	f.email = "frank@" + w.domain

	setLimits(map[string]int64{"maxMailboxes": l.Usage.Mailboxes + 1})
	w.admin.must("PUT", org+"/defaults", map[string]any{"quotaBytes": 1}, nil, 200)
	f.subject = "halo-" + randomSuffix()
	oidcLogin(t, f, slug)
	var boxes []struct {
		Address    string
		QuotaBytes int64
	}
	w.admin.must("GET", org+"/mailboxes", nil, &boxes, 200)
	provisioned := int64(-1)
	for _, b := range boxes {
		if b.Address == f.email {
			provisioned = b.QuotaBytes
		}
	}
	if provisioned != 1 {
		t.Fatalf("OIDC mailbox quota %d, want organization default 1", provisioned)
	}

	setLimits(map[string]int64{"maxMailboxes": l.Usage.Mailboxes + 2})
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Go(func() { codes[i] = w.admin.do("POST", org+"/mailboxes", mailbox("race"+randomSuffix(), nil), nil) })
	}
	wg.Wait()
	accepted := 0
	for _, c := range codes {
		if c == 201 {
			accepted++
		} else if c != 409 {
			t.Fatalf("parallel create returned %d", c)
		}
	}
	if accepted != 1 {
		t.Fatalf("parallel creates for one free slot returned %v, want exactly one 201", codes)
	}
}

//go:build integration

package integration

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type featureSettings struct {
	Features          map[string]bool `json:"features"`
	PlatformAdminOnly []string        `json:"platformAdminOnly"`
}

func TestOrganizationFeatureSwitches(t *testing.T) {
	w := setupWorld(t)
	alice := "alice@" + w.domain
	features := "/organizations/" + w.orgID + "/features"
	w.admin.must("POST", "/organizations/"+w.orgID+"/members", map[string]string{"email": alice, "role": "owner"}, nil, 204)

	var got featureSettings
	w.bob.must("GET", features, nil, &got, 200)
	if len(got.Features) != 8 || slices.Contains(slices.Collect(maps.Values(got.Features)), false) {
		t.Fatalf("new organization features = %v, want all 8 on", got.Features)
	}
	if !slices.Equal(got.PlatformAdminOnly, []string{"externalForwarding", "mailImport", "webhooks"}) {
		t.Fatalf("platform administrator features = %v", got.PlatformAdminOnly)
	}

	if code := w.bob.do("PUT", features, map[string]bool{"calendar": false}, nil); code != 403 {
		t.Fatalf("member changed a feature: %d", code)
	}
	if code := w.alice.do("PUT", features, map[string]bool{"webhooks": false}, nil); code != 403 {
		t.Fatalf("owner changed a platform administrator feature: %d", code)
	}
	if code := w.alice.do("PUT", features, map[string]bool{"teleport": false}, nil); code != 422 {
		t.Fatalf("unknown feature returned %d, want 422", code)
	}
	w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/rules", []map[string]any{{
		"name": "outside", "enabled": true, "match": "all",
		"conditions": []map[string]string{{"field": "subject", "contains": "forward me"}},
		"actions":    map[string]any{"forwardTo": []string{"someone@elsewhere.test"}, "keepCopy": true},
	}}, nil, 200)

	w.alice.must("PUT", features, map[string]bool{"calendar": false, "dav": false, "appPasswords": false, "apiKeys": false, "webhooks": true}, &got, 200)
	w.admin.must("PUT", features, map[string]bool{"webhooks": false, "mailImport": false, "externalForwarding": false}, &got, 200)
	for f, on := range got.Features {
		if want := f == "personalAliases"; on != want {
			t.Errorf("feature %s = %v after switching off, want %v", f, on, want)
		}
	}

	var session struct {
		Memberships []struct {
			OrgID            string   `json:"orgId"`
			DisabledFeatures []string `json:"disabledFeatures"`
		} `json:"memberships"`
		DisabledFeatures []string `json:"disabledFeatures"`
	}
	w.alice.must("GET", "/session", nil, &session, 200)
	if len(session.Memberships) != 1 || len(session.Memberships[0].DisabledFeatures) != 7 || len(session.DisabledFeatures) != 7 {
		t.Fatalf("session does not list the disabled features: %+v", session)
	}

	checks := []struct {
		client       *apiClient
		method, path string
		body         any
	}{
		{w.alice, "GET", "/calendar/events", nil},
		{w.alice, "GET", "/contacts", nil},
		{w.alice, "GET", "/calendar/calendars", nil},
		{w.alice, "POST", "/me/app-passwords", map[string]string{"name": "blocked", "mailboxId": w.aliceMailbox}},
		{w.alice, "GET", "/mailboxes/" + w.aliceMailbox + "/imports", nil},
		{w.admin, "POST", "/mailboxes/" + w.aliceMailbox + "/calendar/import", nil},
		{w.admin, "GET", "/organizations/" + w.orgID + "/webhooks", nil},
		{w.admin, "GET", "/organizations/" + w.orgID + "/api-keys", nil},
		{w.alice, "PUT", "/mailboxes/" + w.aliceMailbox + "/rules", []map[string]any{{
			"name": "new outside", "enabled": true, "match": "all",
			"conditions": []map[string]string{{"field": "subject", "contains": "x"}},
			"actions":    map[string]any{"forwardTo": []string{"another@elsewhere.test"}},
		}}},
	}
	for _, c := range checks {
		if code := c.client.do(c.method, c.path, c.body, nil); code != 403 {
			t.Errorf("%s %s with the feature off = %d, want 403", c.method, c.path, code)
		}
	}
	var rules []struct {
		Actions struct {
			ForwardTo []string `json:"forwardTo"`
		} `json:"actions"`
	}
	w.alice.must("GET", "/mailboxes/"+w.aliceMailbox+"/rules", nil, &rules, 200)
	if len(rules) != 1 || !slices.Equal(rules[0].Actions.ForwardTo, []string{"someone@elsewhere.test"}) {
		t.Fatalf("stored rules were not kept: %+v", rules)
	}
	w.alice.must("PUT", "/mailboxes/"+w.aliceMailbox+"/rules", []map[string]any{{
		"name": "inside", "enabled": true, "match": "all",
		"conditions": []map[string]string{{"field": "subject", "contains": "x"}},
		"actions":    map[string]any{"forwardTo": []string{"bob@" + w.domain}},
	}}, nil, 200)

	if code := davPropfind(t, alice, w.aliceAppPassword); code != 401 {
		t.Errorf("DAV with an app password while app passwords are off = %d, want 401", code)
	}
	if c, err := imapLogin(t, alice, w.aliceAppPassword); err == nil {
		c.Close()
		t.Error("IMAP accepted an app password while app passwords are off")
	}

	var audit []struct {
		Action     string `json:"action"`
		TargetType string `json:"targetType"`
		TargetID   string `json:"targetId"`
	}
	w.admin.must("GET", "/organizations/"+w.orgID+"/audit", nil, &audit, 200)
	disabled := 0
	for _, e := range audit {
		if e.Action == "feature.disable" && e.TargetType == "feature" {
			disabled++
		}
	}
	if disabled != 7 {
		t.Errorf("audit log has %d feature.disable events, want 7", disabled)
	}

	w.admin.must("PUT", features, map[string]bool{"calendar": true, "dav": true, "appPasswords": true, "apiKeys": true, "webhooks": true, "mailImport": true, "externalForwarding": true}, nil, 200)
	w.alice.must("GET", "/calendar/calendars", nil, nil, 200)
	w.admin.must("GET", "/organizations/"+w.orgID+"/webhooks", nil, nil, 200)
	if code := davPropfind(t, alice, w.aliceAppPassword); code != 207 {
		t.Errorf("DAV after switching back on = %d, want 207", code)
	}
	c, err := imapLogin(t, alice, w.aliceAppPassword)
	if err != nil {
		t.Fatalf("IMAP with the kept app password after switching back on: %v", err)
	}
	c.Close()
}

func TestCalendarSwitchFollowsEveryOrganization(t *testing.T) {
	a := setupWorld(t)
	b := setupWorld(t)
	alice := "alice@" + a.domain
	a.admin.must("POST", "/organizations/"+b.orgID+"/members", map[string]string{"email": alice, "role": "member"}, nil, 204)
	a.alice.must("GET", "/calendar/calendars", nil, nil, 200)
	a.admin.must("PUT", "/organizations/"+b.orgID+"/features", map[string]bool{"calendar": false}, nil, 200)
	if code := a.alice.do("GET", "/calendar/calendars", nil, nil); code != 403 {
		t.Fatalf("calendar stayed on although another of the person's organizations turned it off: %d", code)
	}
	if code := davPropfind(t, alice, a.aliceAppPassword); code != 404 {
		t.Fatalf("DAV with calendars off in one organization = %d, want 404", code)
	}
}

func davPropfind(t *testing.T, user, password string) int {
	t.Helper()
	req, _ := http.NewRequest("PROPFIND", env(t, "AETHER_TEST_API", "http://api:8080")+"/dav/", strings.NewReader(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`))
	req.SetBasicAuth(user, password)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

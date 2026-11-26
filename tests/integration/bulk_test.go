//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type bulkResultBody struct {
	Index   int    `json:"index"`
	Type    string `json:"type"`
	Address string `json:"address"`
	Status  string `json:"status"`
	Mailbox *struct {
		ID          string `json:"id"`
		Address     string `json:"address"`
		DisplayName string `json:"displayName"`
		QuotaBytes  int64  `json:"quotaBytes"`
		Kind        string `json:"kind"`
		Active      bool   `json:"active"`
	} `json:"mailbox"`
	Alias *aliasBody `json:"alias"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type bulkResponseBody struct {
	DryRun  bool `json:"dryRun"`
	Summary struct {
		Total     int `json:"total"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	} `json:"summary"`
	Results []bulkResultBody `json:"results"`
}

func TestBulkImportAndAPIKeyScopes(t *testing.T) {
	admin := adminClient(t)
	suffix := randomSuffix()
	var org idOnly
	admin.must("POST", "/organizations", map[string]string{"name": "Bulk " + suffix, "slug": "bulk-" + suffix}, &org, 201)
	domain := "bulk" + suffix + ".test"
	var dom idOnly
	admin.must("POST", "/organizations/"+org.ID+"/domains", map[string]string{"name": domain}, &dom, 201)

	inactive := false
	ops := []map[string]any{
		{"type": "mailbox", "address": "alice@" + domain, "displayName": "Alice", "quotaBytes": 104857600, "password": "alice-bulk-password-123"},
		{"type": "mailbox", "address": "sso@" + domain, "displayName": "SSO User"},
		{"type": "mailbox", "address": "support@" + domain, "displayName": "Support", "kind": "shared", "active": &inactive},
		{"type": "alias", "address": "sales@" + domain, "destinations": []string{"alice@" + domain, "sso@" + domain}},
		{"type": "alias", "address": "@" + domain, "destinations": []string{"alice@" + domain}},
		{
			"type": "list", "address": "staff@" + domain, "name": "Staff",
			"destinations": []string{"alice@" + domain, "sso@" + domain},
			"acceptFrom":   "organization", "sendAs": []string{"alice@" + domain}, "owners": []string{"alice@" + domain},
		},
		{"type": "mailbox", "address": "alice@" + domain, "displayName": "Duplicate"},
		{"type": "mailbox", "address": "weak@" + domain, "password": "short"},
		{"type": "mailbox", "address": "badshared@" + domain, "kind": "shared", "password": "long-enough-password"},
		{"type": "mailbox", "address": "foreign@other.invalid"},
		{"type": "list", "address": "@" + domain, "destinations": []string{"alice@" + domain}},
		{"type": "alias", "address": "badalias@" + domain, "destinations": []string{"alice@" + domain}, "sendAs": []string{"alice@" + domain}},
		{"type": "list", "address": "badowner@" + domain, "destinations": []string{"alice@" + domain}, "owners": []string{"nobody@" + domain}},
	}

	var dry bulkResponseBody
	admin.must("POST", "/organizations/"+org.ID+"/bulk", map[string]any{"dryRun": true, "operations": ops}, &dry, 200)
	if !dry.DryRun || dry.Summary.Total != len(ops) || dry.Summary.Succeeded != 6 || dry.Summary.Failed != 7 {
		t.Fatalf("dryRun summary = %+v", dry.Summary)
	}
	for i := 0; i < 6; i++ {
		if dry.Results[i].Status != "valid" || dry.Results[i].Mailbox != nil || dry.Results[i].Alias != nil {
			t.Fatalf("dryRun[%d] = %+v, want valid without entity", i, dry.Results[i])
		}
	}
	wantCodes := map[int]string{
		6: "address_taken", 7: "weak_password", 8: "shared_no_password", 9: "invalid_domain",
		10: "invalid_local_part", 11: "list_only", 12: "invalid_owners",
	}
	for idx, want := range wantCodes {
		if dry.Results[idx].Status != "failed" || dry.Results[idx].Error == nil || dry.Results[idx].Error.Code != want {
			t.Fatalf("dryRun[%d] = %+v, want %q", idx, dry.Results[idx], want)
		}
	}

	var existingMailboxes []map[string]any
	admin.must("GET", "/organizations/"+org.ID+"/mailboxes", nil, &existingMailboxes, 200)
	if len(existingMailboxes) != 0 {
		t.Fatalf("dryRun created %d mailboxes", len(existingMailboxes))
	}

	var execResp bulkResponseBody
	admin.must("POST", "/organizations/"+org.ID+"/bulk", map[string]any{"dryRun": false, "operations": ops}, &execResp, 200)
	if execResp.DryRun || execResp.Summary.Total != len(ops) || execResp.Summary.Succeeded != 6 || execResp.Summary.Failed != 7 {
		t.Fatalf("exec summary = %+v", execResp.Summary)
	}
	if execResp.Results[0].Status != "created" || execResp.Results[0].Mailbox == nil || execResp.Results[0].Mailbox.QuotaBytes != 104857600 {
		t.Fatalf("exec[0] = %+v", execResp.Results[0])
	}
	if execResp.Results[2].Status != "created" || execResp.Results[2].Mailbox == nil || execResp.Results[2].Mailbox.Kind != "shared" || execResp.Results[2].Mailbox.Active {
		t.Fatalf("exec[2] = %+v", execResp.Results[2])
	}
	if execResp.Results[5].Status != "created" || execResp.Results[5].Alias == nil || len(execResp.Results[5].Alias.Owners) != 1 {
		t.Fatalf("exec[5] = %+v", execResp.Results[5])
	}
	for idx, want := range wantCodes {
		if execResp.Results[idx].Status != "failed" || execResp.Results[idx].Error == nil || execResp.Results[idx].Error.Code != want {
			t.Fatalf("exec[%d] = %+v, want %q", idx, execResp.Results[idx], want)
		}
	}

	alice := newClient(t)
	alice.login("alice@"+domain, "alice-bulk-password-123")
	if code := alice.do("POST", "/organizations/"+org.ID+"/bulk", map[string]any{"dryRun": true, "operations": []map[string]any{{"type": "mailbox", "address": "x@" + domain}}}, nil); code != 403 {
		t.Fatalf("member bulk status = %d, want 403", code)
	}

	var rerun bulkResponseBody
	admin.must("POST", "/organizations/"+org.ID+"/bulk", map[string]any{
		"dryRun": true,
		"operations": []map[string]any{
			{"type": "mailbox", "address": "alice@" + domain},
			{"type": "alias", "address": "sales@" + domain, "destinations": []string{"alice@" + domain}},
		},
	}, &rerun, 200)
	if rerun.Summary.Failed != 2 || rerun.Results[0].Error.Code != "address_taken" || rerun.Results[1].Error.Code != "address_taken" {
		t.Fatalf("rerun conflict check = %+v", rerun)
	}

	makeKey := func(name string, scopes []string) *apiClient {
		var created struct {
			Token string `json:"token"`
		}
		admin.must("POST", "/organizations/"+org.ID+"/api-keys", map[string]any{"name": name, "scopes": scopes}, &created, 201)
		return &apiClient{t: t, base: admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: created.Token}
	}
	mbKey := makeKey("mb-only", []string{"mailboxes:write"})
	aliasKey := makeKey("alias-only", []string{"aliases:write"})
	readKey := makeKey("read-only", []string{"mailboxes:read", "aliases:read"})

	mbReq := map[string]any{"dryRun": true, "operations": []map[string]any{{"type": "mailbox", "address": "k1@" + domain}}}
	aliasReq := map[string]any{"dryRun": true, "operations": []map[string]any{{"type": "alias", "address": "k2@" + domain, "destinations": []string{"alice@" + domain}}}}
	mixedReq := map[string]any{"dryRun": true, "operations": []map[string]any{
		{"type": "mailbox", "address": "k3@" + domain},
		{"type": "alias", "address": "k4@" + domain, "destinations": []string{"alice@" + domain}},
	}}

	mbKey.must("POST", "/organizations/"+org.ID+"/bulk", mbReq, nil, 200)
	if code := mbKey.do("POST", "/organizations/"+org.ID+"/bulk", mixedReq, nil); code != 403 {
		t.Fatalf("mailboxes:write key on mixed bulk = %d, want 403", code)
	}
	aliasKey.must("POST", "/organizations/"+org.ID+"/bulk", aliasReq, nil, 200)
	if code := aliasKey.do("POST", "/organizations/"+org.ID+"/bulk", mbReq, nil); code != 403 {
		t.Fatalf("aliases:write key on mailbox bulk = %d, want 403", code)
	}
	if code := readKey.do("POST", "/organizations/"+org.ID+"/bulk", mbReq, nil); code != 403 {
		t.Fatalf("read-only key on bulk = %d, want 403", code)
	}
}

func TestBulkDryRunReportsPlatformAdminAccounts(t *testing.T) {
	w := setupWorld(t)
	ctx := context.Background()
	db := testDB(t)
	root := "root" + randomSuffix() + "@" + w.domain
	if _, err := db.Exec(ctx, `INSERT INTO users (email, display_name, is_platform_admin) VALUES ($1, 'Root', true)`, root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(ctx, `DELETE FROM users WHERE email = $1`, root) })
	var created struct{ Token string }
	w.admin.must("POST", "/organizations/"+w.orgID+"/api-keys", map[string]any{"name": "bulk", "scopes": []string{"mailboxes:write"}}, &created, 201)
	key := &apiClient{t: t, base: w.admin.base, http: &http.Client{Timeout: 30 * time.Second}, bearer: created.Token}
	ops := []map[string]any{{"type": "mailbox", "address": root, "displayName": "Root"}}
	for _, dryRun := range []bool{true, false} {
		var out bulkResponseBody
		key.must("POST", "/organizations/"+w.orgID+"/bulk", map[string]any{"dryRun": dryRun, "operations": ops}, &out, 200)
		if len(out.Results) != 1 || out.Results[0].Status != "failed" || out.Results[0].Error == nil || out.Results[0].Error.Code != "self_membership" {
			t.Fatalf("dryRun=%v: %+v", dryRun, out.Results)
		}
	}
}

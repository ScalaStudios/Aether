package api

import (
	"slices"
	"testing"
)

func TestResolveBulkAddress(t *testing.T) {
	byID := map[string]string{
		"11111111-1111-1111-1111-111111111111": "example.com",
		"22222222-2222-2222-2222-222222222222": "other.test",
	}
	byName := map[string]string{
		"example.com": "11111111-1111-1111-1111-111111111111",
		"other.test":  "22222222-2222-2222-2222-222222222222",
	}

	id, dom, local, addr, ie := resolveBulkAddress(byID, byName, "  Alice@Example.COM ", "", "")
	if ie != nil || id != "11111111-1111-1111-1111-111111111111" || dom != "example.com" || local != "alice" || addr != "alice@example.com" {
		t.Fatalf("full address = (%q, %q, %q, %q, %+v)", id, dom, local, addr, ie)
	}

	id, dom, local, addr, ie = resolveBulkAddress(byID, byName, "@example.com", "", "")
	if ie != nil || id != "11111111-1111-1111-1111-111111111111" || dom != "example.com" || local != "" || addr != "@example.com" {
		t.Fatalf("catch-all = (%q, %q, %q, %q, %+v)", id, dom, local, addr, ie)
	}

	id, dom, local, addr, ie = resolveBulkAddress(byID, byName, "", "11111111-1111-1111-1111-111111111111", "Bob")
	if ie != nil || id != "11111111-1111-1111-1111-111111111111" || dom != "example.com" || local != "bob" || addr != "bob@example.com" {
		t.Fatalf("domainId+localPart = (%q, %q, %q, %q, %+v)", id, dom, local, addr, ie)
	}

	cases := []struct {
		name      string
		address   string
		domainID  string
		localPart string
		wantCode  string
	}{
		{"missing", "", "", "", "invalid_address"},
		{"no at", "alice", "", "", "invalid_address"},
		{"double at", "a@b@example.com", "", "", "invalid_address"},
		{"foreign domain", "alice@outside.org", "", "", "invalid_domain"},
		{"unknown domainId", "", "33333333-3333-3333-3333-333333333333", "alice", "invalid_domain"},
		{"mismatched domainId", "alice@example.com", "22222222-2222-2222-2222-222222222222", "", "invalid_domain"},
		{"mismatched localPart", "alice@example.com", "", "bob", "invalid_local_part"},
	}
	for _, c := range cases {
		_, _, _, _, ie := resolveBulkAddress(byID, byName, c.address, c.domainID, c.localPart)
		if ie == nil || ie.Code != c.wantCode {
			t.Errorf("%s: got %+v, want code %q", c.name, ie, c.wantCode)
		}
	}
}

func TestNormalizeAndValidateMailbox(t *testing.T) {
	in := mailboxCreateInput{LocalPart: " Alice.Test ", DisplayName: " Alice ", QuotaBytes: 1024, Password: "a-very-long-password"}
	if ie := normalizeAndValidateMailbox(&in); ie != nil {
		t.Fatalf("valid user mailbox rejected: %+v", ie)
	}
	if in.LocalPart != "alice.test" || in.DisplayName != "Alice" || in.Kind != "user" {
		t.Fatalf("unexpected normalization: %+v", in)
	}

	shared := mailboxCreateInput{LocalPart: "support", Kind: "SHARED"}
	if ie := normalizeAndValidateMailbox(&shared); ie != nil || shared.Kind != "shared" {
		t.Fatalf("valid shared mailbox rejected: %+v %+v", shared, ie)
	}

	cases := []struct {
		name     string
		in       mailboxCreateInput
		wantCode string
	}{
		{"invalid kind", mailboxCreateInput{LocalPart: "a", Kind: "group"}, "invalid_kind"},
		{"shared password", mailboxCreateInput{LocalPart: "a", Kind: "shared", Password: "a-very-long-password"}, "shared_no_password"},
		{"empty local", mailboxCreateInput{LocalPart: ""}, "invalid_local_part"},
		{"wildcard local", mailboxCreateInput{LocalPart: "a*b"}, "invalid_local_part"},
		{"negative quota", mailboxCreateInput{LocalPart: "a", QuotaBytes: -1}, "invalid_quota"},
		{"weak password", mailboxCreateInput{LocalPart: "a", Password: "short"}, "weak_password"},
	}
	for _, c := range cases {
		v := c.in
		if ie := normalizeAndValidateMailbox(&v); ie == nil || ie.Code != c.wantCode {
			t.Errorf("%s: got %+v, want code %q", c.name, ie, c.wantCode)
		}
	}
}

func TestParseAddresses(t *testing.T) {
	got, ie := parseAddresses([]string{" Alice@Example.com ", "alice@example.com", "bob@example.com"}, false)
	if ie != nil || !slices.Equal(got, []string{"alice@example.com", "bob@example.com"}) {
		t.Fatalf("got (%v, %+v)", got, ie)
	}

	got, ie = parseAddresses([]string{"@Example.com", "@example.com", "bob@example.com"}, true)
	if ie != nil || !slices.Equal(got, []string{"@example.com", "bob@example.com"}) {
		t.Fatalf("allowDomains got (%v, %+v)", got, ie)
	}

	if _, ie := parseAddresses([]string{"@example.com"}, false); ie == nil || ie.Code != "invalid_address" {
		t.Fatalf("domain accepted when allowDomains=false: %+v", ie)
	}
	if _, ie := parseAddresses([]string{"Alice <alice@example.com>"}, false); ie == nil || ie.Code != "invalid_address" {
		t.Fatalf("display name accepted: %+v", ie)
	}
}

func TestCheckBulkAPIKeyScopes(t *testing.T) {
	mbOps := []bulkOperation{{Type: "mailbox"}}
	aliasOps := []bulkOperation{{Type: "alias"}}
	listOps := []bulkOperation{{Type: "list"}}
	mixedOps := []bulkOperation{{Type: "mailbox"}, {Type: "list"}}
	unknownOps := []bulkOperation{{Type: "other"}}

	mbKey := &apiKeyIdentity{Scopes: []string{"mailboxes:write"}}
	aliasKey := &apiKeyIdentity{Scopes: []string{"aliases:write"}}
	bothKey := &apiKeyIdentity{Scopes: []string{"mailboxes:write", "aliases:write"}}
	readKey := &apiKeyIdentity{Scopes: []string{"mailboxes:read", "aliases:read"}}

	if ie := checkBulkAPIKeyScopes(nil, mixedOps); ie != nil {
		t.Fatalf("session principal rejected: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(mbKey, mbOps); ie != nil {
		t.Fatalf("mailbox key rejected for mailboxes: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(mbKey, mixedOps); ie == nil || ie.Code != "insufficient_scope" {
		t.Fatalf("mailbox key allowed for mixed ops: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(aliasKey, aliasOps); ie != nil {
		t.Fatalf("alias key rejected for aliases: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(aliasKey, listOps); ie != nil {
		t.Fatalf("alias key rejected for lists: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(aliasKey, mbOps); ie == nil || ie.Code != "insufficient_scope" {
		t.Fatalf("alias key allowed for mailboxes: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(bothKey, mixedOps); ie != nil {
		t.Fatalf("both scopes rejected for mixed ops: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(readKey, mbOps); ie == nil || ie.Code != "insufficient_scope" {
		t.Fatalf("read key allowed for mailboxes: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(readKey, unknownOps); ie == nil || ie.Code != "insufficient_scope" {
		t.Fatalf("read key allowed for unknown ops: %+v", ie)
	}
	if ie := checkBulkAPIKeyScopes(mbKey, unknownOps); ie != nil {
		t.Fatalf("write key rejected before per-item unknown type check: %+v", ie)
	}
}

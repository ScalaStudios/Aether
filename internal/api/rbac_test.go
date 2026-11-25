package api

import (
	"net/http"
	"slices"
	"testing"
)

func TestBuildOrgGrantBuiltInRoles(t *testing.T) {
	const org = "11111111-1111-1111-1111-111111111111"
	const domA = "22222222-2222-2222-2222-222222222222"
	const domB = "33333333-3333-3333-3333-333333333333"

	t.Run("domain_admin", func(t *testing.T) {
		p := &principal{grants: map[string]orgGrant{org: buildOrgGrant(roleDomainAdmin, nil, "", nil, []string{domA})}}
		if !p.canOnDomain(org, domA, permMailboxesWrite) || !p.canOnDomain(org, domA, permMailboxesDelegates) || !p.canOnDomain(org, domA, permAliasesWrite) || !p.canOnDomain(org, domA, permSignaturesWrite) || !p.canOnDomain(org, domA, permDomainsRead) {
			t.Fatal("domain_admin missing expected permissions on assigned domain")
		}
		if p.canOnDomain(org, domB, permMailboxesRead) || p.canOnDomain(org, domB, permDomainsRead) {
			t.Fatal("domain_admin allowed outside assigned domain")
		}
		if p.can(org, permDomainsWrite) || p.can(org, permMembersWrite) || p.can(org, permAuditRead) || p.can(org, permRolesWrite) {
			t.Fatal("domain_admin granted org-wide permissions")
		}
		empty := &principal{grants: map[string]orgGrant{org: buildOrgGrant(roleDomainAdmin, nil, "", nil, nil)}}
		if empty.allowsDomain(org, domA) {
			t.Fatal("domain_admin with empty domain list must fail closed")
		}
	})

	t.Run("helpdesk", func(t *testing.T) {
		p := &principal{grants: map[string]orgGrant{org: buildOrgGrant(roleHelpdesk, nil, "", nil, nil)}}
		if !p.can(org, permMailboxesRead) || !p.can(org, permMailboxesSettings) || !p.can(org, permMailboxesAppPass) || !p.can(org, permMembersRead) || !p.can(org, permMembersSupport) {
			t.Fatal("helpdesk missing expected permissions")
		}
		if p.can(org, permMailboxesDelegates) || p.canReadDelegates(org, domA) {
			t.Fatal("helpdesk must never have delegate read or write access")
		}
		if p.can(org, permMailboxesWrite) || p.can(org, permMailboxesImport) || p.can(org, permDomainsRead) || p.can(org, permMembersWrite) || p.can(org, permAuditRead) {
			t.Fatal("helpdesk granted excess permissions")
		}
		if !p.canManageMailboxSettings(org, domA, true) {
			t.Fatal("helpdesk should manage mailbox settings")
		}
	})

	t.Run("list_admin", func(t *testing.T) {
		p := &principal{grants: map[string]orgGrant{org: buildOrgGrant(roleListAdmin, nil, "", nil, nil)}}
		if !p.canReadAlias(org, domA, "list") || !p.canWriteAlias(org, domA, "list") {
			t.Fatal("list_admin should read and write distribution lists")
		}
		if p.canReadAlias(org, domA, "alias") || p.canWriteAlias(org, domA, "alias") {
			t.Fatal("list_admin must not access plain aliases")
		}
		if p.can(org, permMailboxesRead) || p.can(org, permDomainsRead) || p.can(org, permMembersRead) {
			t.Fatal("list_admin granted excess permissions")
		}
	})

	t.Run("auditor", func(t *testing.T) {
		p := &principal{grants: map[string]orgGrant{org: buildOrgGrant(roleAuditor, nil, "", nil, nil)}}
		for _, perm := range []string{
			permDomainsRead, permMailboxesRead, permAliasesRead, permListsRead, permMembersRead,
			permAuditRead, permWebhooksRead, permBrandingRead, permSignaturesRead, permAPIKeysRead, permRolesRead,
		} {
			if !p.can(org, perm) {
				t.Fatalf("auditor missing %s", perm)
			}
		}
		for _, perm := range []string{
			permDomainsWrite, permMailboxesWrite, permMailboxesSettings, permMailboxesAppPass,
			permMailboxesDelegates, permMailboxesImport, permAliasesWrite, permListsWrite,
			permMembersWrite, permMembersSupport, permWebhooksWrite, permBrandingWrite,
			permSignaturesWrite, permAPIKeysWrite, permRolesWrite,
		} {
			if p.can(org, perm) {
				t.Fatalf("auditor must not have %s", perm)
			}
		}
		if !p.canReadDelegates(org, domA) {
			t.Fatal("auditor should read delegates")
		}
		if p.canManageMailboxSettings(org, domA, false) || p.canManageMailboxSettings(org, domA, true) {
			t.Fatal("auditor must not read or write individual mailbox rules/folders/signatures")
		}
	})

	t.Run("custom_role", func(t *testing.T) {
		roleID := "44444444-4444-4444-4444-444444444444"
		p := &principal{grants: map[string]orgGrant{
			org: buildOrgGrant(roleCustom, &roleID, "DNS Ops", []string{"domains:write", "audit:read"}, []string{domA}),
		}}
		if !p.canOnDomain(org, domA, permDomainsWrite) || !p.can(org, permAuditRead) {
			t.Fatal("custom role missing configured permissions")
		}
		if p.canOnDomain(org, domB, permDomainsRead) {
			t.Fatal("domain-scoped custom role allowed outside assigned domain")
		}
		if p.can(org, permMailboxesRead) || p.can(org, permRolesWrite) {
			t.Fatal("custom role granted unconfigured permissions")
		}
	})
}

func TestNormalizeRoleScopes(t *testing.T) {
	got, msg := normalizeRoleScopes([]string{"mailboxes:read", "mailboxes:write", "audit:read", "domains:read"})
	if msg != "" {
		t.Fatalf("unexpected error: %s", msg)
	}
	want := []string{"domains:read", "mailboxes:write", "audit:read"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if _, msg := normalizeRoleScopes(nil); msg == "" {
		t.Fatal("expected error for empty scopes")
	}
	if _, msg := normalizeRoleScopes([]string{"audit:write"}); msg == "" {
		t.Fatal("expected error for audit:write")
	}
	if _, msg := normalizeRoleScopes([]string{"unknown:read"}); msg == "" {
		t.Fatal("expected error for unknown resource")
	}
}

func TestPrivilegeEscalationGuards(t *testing.T) {
	const org = "11111111-1111-1111-1111-111111111111"
	const domA = "22222222-2222-2222-2222-222222222222"
	const domB = "33333333-3333-3333-3333-333333333333"
	roleID := "44444444-4444-4444-4444-444444444444"

	customMemberMgr := &principal{grants: map[string]orgGrant{
		org: buildOrgGrant(roleCustom, &roleID, "Member Manager", []string{"members:write", "mailboxes:read"}, []string{domA}),
	}}
	adminGrant := buildOrgGrant(roleAdmin, nil, "", nil, nil)
	if customMemberMgr.canManageMemberRole(org, roleAdmin, &adminGrant) {
		t.Fatal("custom members:write role must not assign admin")
	}
	domainAdminGrant := buildOrgGrant(roleDomainAdmin, nil, "", nil, []string{domA})
	if customMemberMgr.canManageMemberRole(org, roleDomainAdmin, &domainAdminGrant) {
		t.Fatal("custom role without mailboxes:write must not assign domain_admin")
	}
	narrowGrant := buildOrgGrant(roleCustom, &roleID, "Mailbox Reader", []string{"mailboxes:read"}, []string{domA})
	if !customMemberMgr.canManageMemberRole(org, roleCustom, &narrowGrant) {
		t.Fatal("custom members:write role should assign subset custom role on same domain")
	}
	otherDomainGrant := buildOrgGrant(roleCustom, &roleID, "Mailbox Reader", []string{"mailboxes:read"}, []string{domB})
	if customMemberMgr.canManageMemberRole(org, roleCustom, &otherDomainGrant) {
		t.Fatal("domain-scoped custom role must not assign outside its domain scope")
	}

	helpdesk := &principal{grants: map[string]orgGrant{org: buildOrgGrant(roleHelpdesk, nil, "", nil, nil)}}
	if status, _, _ := helpdesk.canSupportMember(org, roleMember, false, false); status != 0 {
		t.Fatalf("helpdesk should support ordinary single-org member, got %d", status)
	}
	if status, _, _ := helpdesk.canSupportMember(org, roleDomainAdmin, false, false); status != http.StatusForbidden {
		t.Fatalf("helpdesk must not reset password of domain_admin, got %d", status)
	}
	if status, _, _ := helpdesk.canSupportMember(org, roleAdmin, false, false); status != http.StatusForbidden {
		t.Fatalf("helpdesk must not reset password of admin, got %d", status)
	}
	if status, _, _ := helpdesk.canSupportMember(org, roleMember, false, true); status != http.StatusConflict {
		t.Fatalf("helpdesk must not reset password of multi-org user, got %d", status)
	}
	if status, _, _ := helpdesk.canSupportMember(org, roleMember, true, false); status != http.StatusForbidden {
		t.Fatalf("helpdesk must not reset password of platform admin, got %d", status)
	}
}

func TestResolveMappedRoles(t *testing.T) {
	const org = "11111111-1111-1111-1111-111111111111"
	const domA = "22222222-2222-2222-2222-222222222222"
	const domB = "33333333-3333-3333-3333-333333333333"
	const roleID = "44444444-4444-4444-4444-444444444444"

	got := resolveMappedRoles([]string{"eng-a", "eng-b", "auditors"}, []groupMapping{
		{Group: "auditors", OrgID: org, Role: roleAuditor},
		{Group: "eng-a", OrgID: org, Role: roleDomainAdmin, DomainIDs: []string{domA}},
		{Group: "eng-b", OrgID: org, Role: roleDomainAdmin, DomainIDs: []string{domB}},
	})
	if len(got) != 1 || got[0].Role != roleDomainAdmin || !slices.Equal(got[0].DomainIDs, []string{domA, domB}) {
		t.Fatalf("expected merged domain_admin with both domains, got %+v", got)
	}

	gotCustom := resolveMappedRoles([]string{"ops-scoped", "ops-all"}, []groupMapping{
		{Group: "ops-scoped", OrgID: org, Role: roleCustom, CustomRoleID: roleID, DomainIDs: []string{domA}},
		{Group: "ops-all", OrgID: org, Role: roleCustom, CustomRoleID: roleID},
	})
	if len(gotCustom) != 1 || gotCustom[0].Role != roleCustom || !gotCustom[0].OrgWide || len(gotCustom[0].DomainIDs) != 0 {
		t.Fatalf("expected org-wide custom role to win over scoped mapping, got %+v", gotCustom)
	}
}

package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	roleOwner       = "owner"
	roleAdmin       = "admin"
	roleDomainAdmin = "domain_admin"
	roleHelpdesk    = "helpdesk"
	roleListAdmin   = "list_admin"
	roleAuditor     = "auditor"
	roleCustom      = "custom"
	roleMember      = "member"
)

const (
	permDomainsRead        = "domains:read"
	permDomainsWrite       = "domains:write"
	permMailboxesRead      = "mailboxes:read"
	permMailboxesWrite     = "mailboxes:write"
	permMailboxesSettings  = "mailboxes:settings"
	permMailboxesAppPass   = "mailboxes:app_passwords"
	permMailboxesDelegates = "mailboxes:delegates"
	permMailboxesImport    = "mailboxes:import"
	permAliasesRead        = "aliases:read"
	permAliasesWrite       = "aliases:write"
	permListsRead          = "lists:read"
	permListsWrite         = "lists:write"
	permMembersRead        = "members:read"
	permMembersWrite       = "members:write"
	permMembersSupport     = "members:support"
	permAuditRead          = "audit:read"
	permWebhooksRead       = "webhooks:read"
	permWebhooksWrite      = "webhooks:write"
	permBrandingRead       = "branding:read"
	permBrandingWrite      = "branding:write"
	permSignaturesRead     = "signatures:read"
	permSignaturesWrite    = "signatures:write"
	permAPIKeysRead        = "api_keys:read"
	permAPIKeysWrite       = "api_keys:write"
	permRolesRead          = "roles:read"
	permRolesWrite         = "roles:write"
)

var validOrgRoles = []string{
	roleOwner,
	roleAdmin,
	roleDomainAdmin,
	roleHelpdesk,
	roleListAdmin,
	roleAuditor,
	roleCustom,
	roleMember,
}

var allOrgPerms = []string{
	permDomainsRead,
	permDomainsWrite,
	permMailboxesRead,
	permMailboxesWrite,
	permMailboxesSettings,
	permMailboxesAppPass,
	permMailboxesDelegates,
	permMailboxesImport,
	permAliasesRead,
	permAliasesWrite,
	permListsRead,
	permListsWrite,
	permMembersRead,
	permMembersWrite,
	permMembersSupport,
	permAuditRead,
	permWebhooksRead,
	permWebhooksWrite,
	permBrandingRead,
	permBrandingWrite,
	permSignaturesRead,
	permSignaturesWrite,
	permAPIKeysRead,
	permAPIKeysWrite,
	permRolesRead,
	permRolesWrite,
}

type orgGrant struct {
	Role           string
	CustomRoleID   *string
	CustomRoleName string
	CustomScopes   []string
	DomainIDs      []string
	Perms          map[string]bool
	DomainScoped   bool
}

func isValidOrgRole(role string) bool {
	return slices.Contains(validOrgRoles, role)
}

func roleRank(role string) int {
	switch role {
	case roleMember:
		return 1
	case roleAuditor:
		return 2
	case roleListAdmin:
		return 3
	case roleHelpdesk:
		return 4
	case roleDomainAdmin:
		return 5
	case roleCustom:
		return 6
	case roleAdmin:
		return 7
	case roleOwner:
		return 8
	default:
		return 0
	}
}

func buildOrgGrant(role string, customRoleID *string, customRoleName string, customScopes, domainIDs []string) orgGrant {
	g := orgGrant{
		Role:           role,
		CustomRoleID:   customRoleID,
		CustomRoleName: customRoleName,
		CustomScopes:   nonNil(customScopes),
		DomainIDs:      nonNil(domainIDs),
		Perms:          map[string]bool{},
	}
	grant := func(perms ...string) {
		for _, perm := range perms {
			g.Perms[perm] = true
		}
	}
	switch role {
	case roleOwner, roleAdmin:
		grant(allOrgPerms...)
	case roleDomainAdmin:
		g.DomainScoped = true
		grant(
			permDomainsRead,
			permMailboxesRead,
			permMailboxesWrite,
			permMailboxesSettings,
			permMailboxesAppPass,
			permMailboxesDelegates,
			permMailboxesImport,
			permAliasesRead,
			permAliasesWrite,
			permListsRead,
			permListsWrite,
			permSignaturesRead,
			permSignaturesWrite,
		)
	case roleHelpdesk:
		grant(
			permMailboxesRead,
			permMailboxesSettings,
			permMailboxesAppPass,
			permMembersRead,
			permMembersSupport,
		)
	case roleListAdmin:
		grant(permListsRead, permListsWrite)
	case roleAuditor:
		grant(
			permDomainsRead,
			permMailboxesRead,
			permAliasesRead,
			permListsRead,
			permMembersRead,
			permAuditRead,
			permWebhooksRead,
			permBrandingRead,
			permSignaturesRead,
			permAPIKeysRead,
			permRolesRead,
		)
	case roleCustom:
		g.DomainScoped = len(g.DomainIDs) > 0
		for _, scope := range customScopes {
			switch scope {
			case "domains:read":
				grant(permDomainsRead)
			case "domains:write":
				grant(permDomainsRead, permDomainsWrite)
			case "mailboxes:read":
				grant(permMailboxesRead)
			case "mailboxes:write":
				grant(
					permMailboxesRead,
					permMailboxesWrite,
					permMailboxesSettings,
					permMailboxesAppPass,
					permMailboxesDelegates,
					permMailboxesImport,
				)
			case "aliases:read":
				grant(permAliasesRead, permListsRead)
			case "aliases:write":
				grant(permAliasesRead, permAliasesWrite, permListsRead, permListsWrite)
			case "members:read":
				grant(permMembersRead, permRolesRead)
			case "members:write":
				grant(permMembersRead, permMembersWrite, permMembersSupport, permRolesRead)
			case "audit:read":
				grant(permAuditRead)
			case "webhooks:read":
				grant(permWebhooksRead)
			case "webhooks:write":
				grant(permWebhooksRead, permWebhooksWrite)
			}
		}
	}
	return g
}

func (g orgGrant) permissionList() []string {
	out := make([]string, 0, len(g.Perms))
	for _, perm := range allOrgPerms {
		if g.Perms[perm] {
			out = append(out, perm)
		}
	}
	return out
}

func normalizeRoleScopes(scopes []string) ([]string, string) {
	if len(scopes) == 0 {
		return nil, "Choose at least one permission."
	}
	levels := map[string]string{}
	for _, scope := range scopes {
		resource, access, ok := strings.Cut(strings.TrimSpace(scope), ":")
		if !ok || !slices.Contains(apiKeyResources, resource) || (access != "read" && access != "write") {
			return nil, scope + " is not a valid permission."
		}
		if resource == "audit" && access != "read" {
			return nil, "Audit log only supports read access."
		}
		if levels[resource] != "write" {
			levels[resource] = access
		}
	}
	out := make([]string, 0, len(levels))
	for _, resource := range apiKeyResources {
		if access, ok := levels[resource]; ok {
			out = append(out, resource+":"+access)
		}
	}
	return out, ""
}

func (p *principal) orgGrant(orgID string) (orgGrant, bool) {
	if p.User.IsPlatformAdmin {
		return buildOrgGrant(roleOwner, nil, "", nil, nil), true
	}
	g, ok := p.grants[orgID]
	return g, ok
}

func (p *principal) memberCan(orgID, domainID, perm string) bool {
	g, ok := p.grants[orgID]
	return ok && g.Perms[perm] && (!g.DomainScoped || slices.Contains(g.DomainIDs, domainID))
}

func (p *principal) memberAdmin(orgID string) bool {
	role := p.grants[orgID].Role
	return role == roleOwner || role == roleAdmin
}

func (p *principal) grantsOwnMembership(userID string, targetIsPlatformAdmin bool) bool {
	if p.apiKey != nil {
		return targetIsPlatformAdmin
	}
	return p.User.IsPlatformAdmin && userID == p.User.ID
}

func raisesGrant(current *orgGrant, next orgGrant) bool {
	if current == nil || roleRank(next.Role) > roleRank(current.Role) {
		return true
	}
	for perm := range next.Perms {
		if !current.Perms[perm] {
			return true
		}
	}
	if !current.DomainScoped {
		return false
	}
	if !next.DomainScoped {
		return true
	}
	for _, id := range next.DomainIDs {
		if !slices.Contains(current.DomainIDs, id) {
			return true
		}
	}
	return false
}

func selfMembershipError(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, selfMembershipCode, selfMembershipMessage)
}

const (
	selfMembershipCode    = "self_membership"
	selfMembershipMessage = "Platform administrators cannot give themselves a role in an organization. Ask an owner or administrator of the organization."
)

func (p *principal) orgRole(orgID string) string {
	g, ok := p.orgGrant(orgID)
	if !ok {
		return ""
	}
	return g.Role
}

func (p *principal) canAdminOrg(orgID string) bool {
	role := p.orgRole(orgID)
	return role == roleOwner || role == roleAdmin
}

func (p *principal) can(orgID, perm string) bool {
	g, ok := p.orgGrant(orgID)
	return ok && g.Perms[perm]
}

func (p *principal) canAny(orgID string, perms ...string) bool {
	g, ok := p.orgGrant(orgID)
	if !ok {
		return false
	}
	for _, perm := range perms {
		if g.Perms[perm] {
			return true
		}
	}
	return false
}

func (p *principal) allowsDomain(orgID, domainID string) bool {
	g, ok := p.orgGrant(orgID)
	if !ok {
		return false
	}
	if !g.DomainScoped {
		return true
	}
	return slices.Contains(g.DomainIDs, domainID)
}

func (p *principal) canOnDomain(orgID, domainID, perm string) bool {
	return p.can(orgID, perm) && p.allowsDomain(orgID, domainID)
}

func (p *principal) scopedDomains(orgID string) ([]string, bool) {
	g, ok := p.orgGrant(orgID)
	if !ok || !g.DomainScoped {
		return nil, false
	}
	return nonNil(g.DomainIDs), true
}

func (p *principal) canReadDelegates(orgID, domainID string) bool {
	g, ok := p.orgGrant(orgID)
	if !ok || g.Role == roleHelpdesk {
		return false
	}
	return (g.Perms[permMailboxesDelegates] || g.Perms[permMailboxesRead]) && p.allowsDomain(orgID, domainID)
}

func (p *principal) canManageMailboxSettings(orgID, domainID string, write bool) bool {
	g, ok := p.orgGrant(orgID)
	if !ok || !p.allowsDomain(orgID, domainID) {
		return false
	}
	if write {
		return g.Perms[permMailboxesSettings]
	}
	if g.Perms[permMailboxesSettings] {
		return true
	}
	return (g.Role == roleCustom || p.apiKey != nil) && g.Perms[permMailboxesRead]
}

func (p *principal) canReadAlias(orgID, domainID, kind string) bool {
	if !p.allowsDomain(orgID, domainID) {
		return false
	}
	if kind == "list" {
		return p.canAny(orgID, permAliasesRead, permListsRead)
	}
	return p.can(orgID, permAliasesRead)
}

func (p *principal) canWriteAlias(orgID, domainID, kind string) bool {
	if !p.allowsDomain(orgID, domainID) {
		return false
	}
	if kind == "list" {
		return p.canAny(orgID, permAliasesWrite, permListsWrite)
	}
	return p.can(orgID, permAliasesWrite)
}

func (p *principal) canManageMemberRole(orgID, targetRole string, targetGrant *orgGrant) bool {
	if p.User.IsPlatformAdmin {
		return true
	}
	caller, ok := p.orgGrant(orgID)
	if !ok || !caller.Perms[permMembersWrite] {
		return false
	}
	if caller.Role == roleOwner {
		return true
	}
	if caller.Role == roleAdmin {
		return targetRole != roleOwner
	}
	if targetRole == roleOwner || targetRole == roleAdmin {
		return false
	}
	if targetRole == roleMember {
		return true
	}
	if targetGrant == nil {
		return false
	}
	for perm := range targetGrant.Perms {
		if !caller.Perms[perm] {
			return false
		}
	}
	if caller.DomainScoped {
		if !targetGrant.DomainScoped {
			return false
		}
		for _, id := range targetGrant.DomainIDs {
			if !slices.Contains(caller.DomainIDs, id) {
				return false
			}
		}
	}
	return true
}

func (p *principal) canSupportMember(orgID, targetRole string, targetIsPlatformAdmin, targetMultiOrg bool) (int, string, string) {
	if p.User.IsPlatformAdmin {
		return 0, "", ""
	}
	caller, ok := p.orgGrant(orgID)
	if !ok || !caller.Perms[permMembersSupport] {
		return http.StatusForbidden, "forbidden", "You do not have permission to do this."
	}
	if targetIsPlatformAdmin {
		return http.StatusForbidden, "forbidden", "Only a platform administrator can change a platform administrator's account."
	}
	if targetMultiOrg {
		return http.StatusConflict, "multi_org_user", "This account belongs to more than one organization. Ask a platform administrator to change its password or status."
	}
	switch caller.Role {
	case roleOwner:
		return 0, "", ""
	case roleAdmin:
		if targetRole == roleOwner {
			return http.StatusForbidden, "forbidden", "Only an organization owner can change an owner's account."
		}
		return 0, "", ""
	default:
		if targetRole != roleMember {
			return http.StatusForbidden, "forbidden", "You can only reset passwords or change account status for ordinary members."
		}
		return 0, "", ""
	}
}

func (s *Server) loadUserGrants(ctx context.Context, userID string) (map[string]orgGrant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.org_id, m.role, m.custom_role_id, coalesce(r.name, ''), coalesce(r.scopes, '{}'),
			coalesce((SELECT array_agg(d.domain_id::text ORDER BY d.domain_id) FROM organization_member_domains d WHERE d.org_id = m.org_id AND d.user_id = m.user_id), '{}')
		FROM organization_members m
		LEFT JOIN organization_roles r ON r.id = m.custom_role_id
		WHERE m.user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := map[string]orgGrant{}
	for rows.Next() {
		var orgID, role, customName string
		var customID *string
		var scopes, domainIDs []string
		if err := rows.Scan(&orgID, &role, &customID, &customName, &scopes, &domainIDs); err != nil {
			return nil, err
		}
		grants[orgID] = buildOrgGrant(role, customID, customName, scopes, domainIDs)
	}
	return grants, rows.Err()
}

func (s *Server) loadMemberGrant(ctx context.Context, orgID, userID string) (*orgGrant, error) {
	var role, customName string
	var customID *string
	var scopes, domainIDs []string
	err := s.db.QueryRow(ctx, `
		SELECT m.role, m.custom_role_id, coalesce(r.name, ''), coalesce(r.scopes, '{}'),
			coalesce((SELECT array_agg(d.domain_id::text ORDER BY d.domain_id) FROM organization_member_domains d WHERE d.org_id = m.org_id AND d.user_id = m.user_id), '{}')
		FROM organization_members m
		LEFT JOIN organization_roles r ON r.id = m.custom_role_id
		WHERE m.org_id = $1 AND m.user_id = $2`, orgID, userID).Scan(&role, &customID, &customName, &scopes, &domainIDs)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	g := buildOrgGrant(role, customID, customName, scopes, domainIDs)
	return &g, nil
}

func (s *Server) orgPerm(w http.ResponseWriter, r *http.Request, p *principal, perms ...string) (string, bool) {
	orgID, ok := pathID(w, r, "orgId")
	if !ok {
		return "", false
	}
	if p.orgRole(orgID) == "" {
		notFound(w)
		return "", false
	}
	if len(perms) > 0 && !p.canAny(orgID, perms...) {
		forbidden(w)
		return "", false
	}
	return orgID, true
}

func (s *Server) validateOrgDomains(ctx context.Context, orgID string, ids []string) ([]string, bool, error) {
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if !uuidPattern.MatchString(id) {
			return nil, false, nil
		}
		if !slices.Contains(clean, id) {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return clean, true, nil
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM domains WHERE org_id = $1 AND id::text = ANY($2)`, orgID, clean).Scan(&count); err != nil {
		return nil, false, err
	}
	return clean, count == len(clean), nil
}

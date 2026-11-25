package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/scalastudios/aether/internal/auth"
)

type organization struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	CreatedAt    time.Time `json:"createdAt"`
	SpamLearning bool      `json:"spamLearning"`

	DeletedItemRetentionDays int      `json:"deletedItemRetentionDays"`
	MailboxProtocols         []string `json:"mailboxProtocols"`
}

const organizationColumns = `id, name, slug, created_at, spam_learning, deleted_item_retention_days, mailbox_protocols`

func (s *Server) listOrganizations(w http.ResponseWriter, r *http.Request, p *principal) {
	rows, err := s.db.Query(r.Context(), `
		SELECT `+organizationColumns+` FROM organizations o
		WHERE $2 OR EXISTS (SELECT 1 FROM organization_members m WHERE m.org_id = o.id AND m.user_id = $1)
		ORDER BY name`, p.User.ID, p.User.IsPlatformAdmin)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	orgs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[organization])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(orgs))
}

func (s *Server) createOrganization(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 || !slugPattern.MatchString(in.Slug) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_organization", "Enter a name and a slug made of lowercase letters, digits, and hyphens.")
		return
	}
	var org organization
	err := s.db.QueryRow(r.Context(), `INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING `+organizationColumns, in.Name, in.Slug).
		Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedAt, &org.SpamLearning, &org.DeletedItemRetentionDays, &org.MailboxProtocols)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "slug_taken", "Another organization already uses this slug.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &org.ID, "organization.create", "organization", org.ID, map[string]any{"slug": org.Slug})
	writeJSON(w, http.StatusCreated, org)
}

func (s *Server) orgFromPath(w http.ResponseWriter, r *http.Request, p *principal, admin bool) (string, bool) {
	orgID, ok := pathID(w, r, "orgId")
	if !ok {
		return "", false
	}
	if p.orgRole(orgID) == "" {
		notFound(w)
		return "", false
	}
	if admin && !p.canAdminOrg(orgID) {
		forbidden(w)
		return "", false
	}
	return orgID, true
}

func (s *Server) getOrganization(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, false)
	if !ok {
		return
	}
	var org organization
	err := s.db.QueryRow(r.Context(), `SELECT `+organizationColumns+` FROM organizations WHERE id = $1`, orgID).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedAt, &org.SpamLearning, &org.DeletedItemRetentionDays, &org.MailboxProtocols)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (s *Server) updateOrganization(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		SpamLearning             *bool     `json:"spamLearning"`
		DeletedItemRetentionDays *int      `json:"deletedItemRetentionDays"`
		MailboxProtocols         *[]string `json:"mailboxProtocols"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if in.SpamLearning == nil && in.DeletedItemRetentionDays == nil && in.MailboxProtocols == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_organization", "Choose whether the spam filter learns from Junk moves.")
		return
	}
	if in.DeletedItemRetentionDays != nil && (*in.DeletedItemRetentionDays < 1 || *in.DeletedItemRetentionDays > 30) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_retention", "Keep deleted items for 1 to 30 days.")
		return
	}
	if in.MailboxProtocols != nil && !validProtocols(*in.MailboxProtocols) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_protocols", "Protocols must be a list of imap, smtp, sieve, dav and webmail.")
		return
	}
	var org organization
	err := s.db.QueryRow(r.Context(), `
		UPDATE organizations SET spam_learning = coalesce($2, spam_learning), deleted_item_retention_days = coalesce($3, deleted_item_retention_days),
			mailbox_protocols = coalesce($4, mailbox_protocols)
		WHERE id = $1 RETURNING `+organizationColumns, orgID, in.SpamLearning, in.DeletedItemRetentionDays, in.MailboxProtocols).
		Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedAt, &org.SpamLearning, &org.DeletedItemRetentionDays, &org.MailboxProtocols)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	changes := map[string]any{}
	if in.SpamLearning != nil {
		changes["spamLearning"] = org.SpamLearning
	}
	if in.DeletedItemRetentionDays != nil {
		changes["deletedItemRetentionDays"] = org.DeletedItemRetentionDays
	}
	if in.MailboxProtocols != nil {
		changes["mailboxProtocols"] = org.MailboxProtocols
	}
	s.audit(r.Context(), r, p, &org.ID, "organization.update", "organization", org.ID, changes)
	writeJSON(w, http.StatusOK, org)
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permMembersRead, permAliasesWrite, permListsWrite)
	if !ok {
		return
	}
	type member struct {
		UserID          string   `json:"userId"`
		Email           string   `json:"email"`
		DisplayName     string   `json:"displayName"`
		Role            string   `json:"role"`
		CustomRoleID    *string  `json:"customRoleId,omitempty"`
		CustomRoleName  string   `json:"customRoleName,omitempty"`
		DomainIDs       []string `json:"domainIds"`
		Status          string   `json:"status"`
		HasPassword     bool     `json:"hasPassword"`
		IsPlatformAdmin bool     `json:"isPlatformAdmin"`
		MultiOrg        bool     `json:"multiOrg"`
		Compliance      bool     `json:"compliance"`
		PasswordSignIn  bool     `json:"passwordSignIn"`
		TwoFactor       bool     `json:"twoFactor"`
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT u.id, u.email, u.display_name, m.role, m.custom_role_id, coalesce(r.name, ''),
			coalesce((SELECT array_agg(d.domain_id::text ORDER BY d.domain_id) FROM organization_member_domains d WHERE d.org_id = m.org_id AND d.user_id = m.user_id), '{}'),
			u.status, u.password_hash IS NOT NULL, u.is_platform_admin,
			EXISTS (SELECT 1 FROM organization_members other WHERE other.user_id = u.id AND other.org_id <> m.org_id),
			m.compliance, u.password_hash IS NOT NULL, `+hasTwoFactor+`
		FROM organization_members m
		JOIN users u ON u.id = m.user_id
		LEFT JOIN organization_roles r ON r.id = m.custom_role_id
		WHERE m.org_id = $1 ORDER BY u.email`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	members, err := pgx.CollectRows(rows, pgx.RowToStructByPos[member])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for i := range members {
		members[i].DomainIDs = nonNil(members[i].DomainIDs)
	}
	writeJSON(w, http.StatusOK, nonNil(members))
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permAuditRead)
	if !ok {
		return
	}
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil || before <= 0 {
		before = 1<<63 - 1
	}
	type event struct {
		ID         int64     `json:"id"`
		CreatedAt  time.Time `json:"createdAt"`
		ActorEmail *string   `json:"actorEmail"`
		Action     string    `json:"action"`
		TargetType string    `json:"targetType"`
		TargetID   string    `json:"targetId"`
		IP         string    `json:"ip"`
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT e.id, e.created_at, u.email, e.action, e.target_type, e.target_id, e.ip
		FROM audit_events e LEFT JOIN users u ON u.id = e.actor_user_id
		WHERE e.org_id = $1 AND e.id < $2 ORDER BY e.id DESC LIMIT 50`, orgID, before)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[event])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(events))
}

func (s *Server) setMember(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permMembersWrite)
	if !ok {
		return
	}
	var in struct {
		Email        string   `json:"email"`
		Role         string   `json:"role"`
		CustomRoleID string   `json:"customRoleId"`
		DomainIDs    []string `json:"domainIds"`
		Compliance   *bool    `json:"compliance"`
	}
	if !decode(w, r, 16<<10, &in) {
		return
	}
	if !isValidOrgRole(in.Role) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_role", "Choose a valid organization role.")
		return
	}
	var customRoleID *string
	var customRoleName string
	var customScopes []string
	if in.Role == roleCustom {
		cid := strings.ToLower(strings.TrimSpace(in.CustomRoleID))
		if !uuidPattern.MatchString(cid) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_role", "Choose a custom role that belongs to this organization.")
			return
		}
		err := s.db.QueryRow(r.Context(), `SELECT name, scopes FROM organization_roles WHERE id = $1 AND org_id = $2`, cid, orgID).Scan(&customRoleName, &customScopes)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_role", "Choose a custom role that belongs to this organization.")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		customRoleID = &cid
	} else if strings.TrimSpace(in.CustomRoleID) != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_role", "Only the custom role accepts a customRoleId.")
		return
	}
	domainIDs, domainsValid, err := s.validateOrgDomains(r.Context(), orgID, in.DomainIDs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !domainsValid {
		writeError(w, http.StatusUnprocessableEntity, "invalid_domains", "Choose domains that belong to this organization.")
		return
	}
	if in.Role == roleDomainAdmin && len(domainIDs) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_domains", "Choose at least one domain for a domain administrator.")
		return
	}
	if in.Role != roleDomainAdmin && in.Role != roleCustom && len(domainIDs) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_domains", "Only domain administrators and custom roles can be scoped to specific domains.")
		return
	}
	targetGrant := buildOrgGrant(in.Role, customRoleID, customRoleName, customScopes, domainIDs)
	if !p.canManageMemberRole(orgID, in.Role, &targetGrant) {
		forbidden(w)
		return
	}
	var userID string
	err = s.db.QueryRow(r.Context(), `SELECT id FROM users WHERE email = $1`, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnprocessableEntity, "unknown_user", "No account uses this email address. Create a mailbox for it or let the person sign in through your identity provider first.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	currentGrant, err := s.loadMemberGrant(r.Context(), orgID, userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var compliance bool
	if err := s.db.QueryRow(r.Context(), `SELECT compliance FROM organization_members WHERE org_id = $1 AND user_id = $2`, orgID, userID).Scan(&compliance); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	complianceChanged := in.Compliance != nil && *in.Compliance != compliance
	if complianceChanged && p.orgRole(orgID) != roleOwner {
		forbidden(w)
		return
	}
	var previous string
	if currentGrant != nil {
		previous = currentGrant.Role
		if !p.canManageMemberRole(orgID, currentGrant.Role, currentGrant) {
			forbidden(w)
			return
		}
		if currentGrant.Role == roleOwner && in.Role != roleOwner {
			var owners int
			if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM organization_members WHERE org_id = $1 AND role = 'owner'`, orgID).Scan(&owners); err != nil {
				s.internalError(w, r, err)
				return
			}
			if owners == 1 {
				writeError(w, http.StatusConflict, "last_owner", "An organization needs at least one owner. Make someone else an owner first.")
				return
			}
		}
	}
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO organization_members (org_id, user_id, role, custom_role_id, compliance) VALUES ($1, $2, $3, $4, coalesce($5, false))
			ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role, custom_role_id = EXCLUDED.custom_role_id, compliance = coalesce($5, organization_members.compliance)`,
			orgID, userID, in.Role, customRoleID, in.Compliance); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM organization_member_domains WHERE org_id = $1 AND user_id = $2`, orgID, userID); err != nil {
			return err
		}
		if len(domainIDs) > 0 {
			if _, err := tx.Exec(r.Context(), `INSERT INTO organization_member_domains (org_id, user_id, domain_id) SELECT $1, $2, unnest($3::uuid[]) ON CONFLICT DO NOTHING`, orgID, userID, domainIDs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	meta := map[string]any{"role": in.Role, "previous": previous}
	if customRoleID != nil {
		meta["customRoleId"] = *customRoleID
		meta["customRoleName"] = customRoleName
	}
	if len(domainIDs) > 0 {
		meta["domainIds"] = domainIDs
	}
	s.audit(r.Context(), r, p, &orgID, "member.set_role", "user", userID, meta)
	if complianceChanged {
		s.audit(r.Context(), r, p, &orgID, "member.set_compliance", "user", userID, map[string]any{"compliance": *in.Compliance})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permMembersWrite)
	if !ok {
		return
	}
	userID, ok := pathID(w, r, "userId")
	if !ok {
		return
	}
	var role, email string
	var owners int
	err := s.db.QueryRow(r.Context(), `SELECT om.role, (SELECT count(*) FROM organization_members WHERE org_id = $1 AND role = 'owner'), u.email FROM organization_members om JOIN users u ON u.id = om.user_id WHERE om.org_id = $1 AND om.user_id = $2`, orgID, userID).Scan(&role, &owners, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	currentGrant, err := s.loadMemberGrant(r.Context(), orgID, userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !p.canManageMemberRole(orgID, role, currentGrant) {
		forbidden(w)
		return
	}
	if role == roleOwner && owners == 1 {
		writeError(w, http.StatusConflict, "last_owner", "An organization needs at least one owner. Make someone else an owner first.")
		return
	}
	type removed struct{ ID, Address string }
	var mailboxes []removed
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM organization_members WHERE org_id = $1 AND user_id = $2`, orgID, userID); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `DELETE FROM mailbox_delegates md USING mailboxes m WHERE m.id = md.mailbox_id AND m.org_id = $1 AND md.user_id = $2 RETURNING m.id, m.address`, orgID, userID)
		if err != nil {
			return err
		}
		if mailboxes, err = pgx.CollectRows(rows, pgx.RowToStructByPos[removed]); err != nil {
			return err
		}
		var ids []string
		for _, m := range mailboxes {
			ids = append(ids, m.ID)
		}
		if err := s.revokeMailAccess(r.Context(), tx, userID, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM alias_owners WHERE user_id = $2 AND alias_id IN (SELECT id FROM aliases WHERE org_id = $1)`, orgID, userID); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `DELETE FROM alias_moderators WHERE user_id = $2 AND alias_id IN (SELECT id FROM aliases WHERE org_id = $1)`, orgID, userID)
		return err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "member.remove", "user", userID, map[string]any{"role": role})
	for _, m := range mailboxes {
		s.audit(r.Context(), r, p, &orgID, "mailbox.delegate_remove", "mailbox", m.ID, map[string]any{"address": m.Address, "delegate": email})
	}
	for _, m := range mailboxes {
		if err := s.syncMailboxACL(r.Context(), m.ID); err != nil {
			s.log.Warn("mailbox ACL sync failed; retrying", "mailbox", m.ID, "err", err)
		}
	}
	wake(s.kickWake)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetMemberPassword(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permMembersSupport)
	if !ok {
		return
	}
	userID, ok := pathID(w, r, "userId")
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	if len(in.Password) < 12 {
		writeError(w, http.StatusUnprocessableEntity, "weak_password", "Use a password of at least 12 characters.")
		return
	}
	var role, status, email string
	var isPlatformAdmin, multiOrg bool
	err := s.db.QueryRow(r.Context(), `
		SELECT om.role, u.status, u.email, u.is_platform_admin,
			EXISTS (SELECT 1 FROM organization_members other WHERE other.user_id = u.id AND other.org_id <> om.org_id)
		FROM organization_members om JOIN users u ON u.id = om.user_id
		WHERE om.org_id = $1 AND om.user_id = $2`, orgID, userID).Scan(&role, &status, &email, &isPlatformAdmin, &multiOrg)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if status == "deprovisioned" {
		writeError(w, http.StatusConflict, "account_deprovisioned", "This account is deprovisioned and cannot sign in.")
		return
	}
	if codeStatus, code, msg := p.canSupportMember(orgID, role, isPlatformAdmin, multiOrg); codeStatus != 0 {
		writeError(w, codeStatus, code, msg)
		return
	}
	hash := auth.HashPassword(in.Password)
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, hash); err != nil {
			return err
		}
		if userID == p.User.ID && len(p.sessionHash) > 0 {
			_, err = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1 AND id_hash <> $2`, userID, p.sessionHash)
			return err
		}
		_, err = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1`, userID)
		return err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "member.password_reset", "user", userID, map[string]any{"email": email})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setMemberStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permMembersSupport)
	if !ok {
		return
	}
	userID, ok := pathID(w, r, "userId")
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if in.Status != "active" && in.Status != "suspended" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_status", "Status must be active or suspended.")
		return
	}
	if userID == p.User.ID {
		writeError(w, http.StatusUnprocessableEntity, "self_suspend", "You cannot change the status of your own account.")
		return
	}
	var role, suspendedBy, email string
	var isPlatformAdmin, multiOrg bool
	err := s.db.QueryRow(r.Context(), `
		SELECT om.role, u.suspended_by, u.email, u.is_platform_admin,
			EXISTS (SELECT 1 FROM organization_members other WHERE other.user_id = u.id AND other.org_id <> om.org_id)
		FROM organization_members om JOIN users u ON u.id = om.user_id
		WHERE om.org_id = $1 AND om.user_id = $2`, orgID, userID).Scan(&role, &suspendedBy, &email, &isPlatformAdmin, &multiOrg)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if suspendedBy == "mailbox-conversion" {
		writeError(w, http.StatusConflict, "mailbox_converted", "This account's mailbox was converted to a shared mailbox. Convert it back to a user mailbox to restore the account.")
		return
	}
	if codeStatus, code, msg := p.canSupportMember(orgID, role, isPlatformAdmin, multiOrg); codeStatus != 0 {
		writeError(w, codeStatus, code, msg)
		return
	}
	changed, err := s.setUserStatus(r.Context(), userID, in.Status, "admin")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if changed {
		s.audit(r.Context(), r, p, &orgID, "user."+map[string]string{"active": "restore", "suspended": "suspend"}[in.Status], "user", userID, map[string]any{"email": email, "source": "console"})
	}
	w.WriteHeader(http.StatusNoContent)
}

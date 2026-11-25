package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	type member struct {
		UserID         string `json:"userId"`
		Email          string `json:"email"`
		DisplayName    string `json:"displayName"`
		Role           string `json:"role"`
		Status         string `json:"status"`
		Compliance     bool   `json:"compliance"`
		PasswordSignIn bool   `json:"passwordSignIn"`
		TwoFactor      bool   `json:"twoFactor"`
	}
	rows, err := s.db.Query(r.Context(), `SELECT u.id, u.email, u.display_name, m.role, u.status, m.compliance, u.password_hash IS NOT NULL, `+hasTwoFactor+` FROM organization_members m JOIN users u ON u.id = m.user_id WHERE m.org_id = $1 ORDER BY u.email`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	members, err := pgx.CollectRows(rows, pgx.RowToStructByPos[member])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(members))
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
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
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		Email      string `json:"email"`
		Role       string `json:"role"`
		Compliance *bool  `json:"compliance"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if in.Role != "owner" && in.Role != "admin" && in.Role != "member" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_role", "Role must be owner, admin, or member.")
		return
	}
	if in.Role == "owner" && p.orgRole(orgID) != "owner" {
		forbidden(w)
		return
	}
	var userID string
	err := s.db.QueryRow(r.Context(), `SELECT id FROM users WHERE email = $1`, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnprocessableEntity, "unknown_user", "No account uses this email address. Create a mailbox for it or let the person sign in through your identity provider first.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var current string
	var compliance bool
	s.db.QueryRow(r.Context(), `SELECT role, compliance FROM organization_members WHERE org_id = $1 AND user_id = $2`, orgID, userID).Scan(&current, &compliance)
	complianceChanged := in.Compliance != nil && *in.Compliance != compliance
	if (current == "owner" || complianceChanged) && p.orgRole(orgID) != "owner" {
		forbidden(w)
		return
	}
	if _, err := s.db.Exec(r.Context(), `INSERT INTO organization_members (org_id, user_id, role, compliance) VALUES ($1, $2, $3, coalesce($4, false)) ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role, compliance = coalesce($4, organization_members.compliance)`, orgID, userID, in.Role, in.Compliance); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "member.set_role", "user", userID, map[string]any{"role": in.Role, "previous": current})
	if complianceChanged {
		s.audit(r.Context(), r, p, &orgID, "member.set_compliance", "user", userID, map[string]any{"compliance": *in.Compliance})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
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
	if role == "owner" && p.orgRole(orgID) != "owner" {
		forbidden(w)
		return
	}
	if role == "owner" && owners == 1 {
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

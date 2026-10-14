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
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) listOrganizations(w http.ResponseWriter, r *http.Request, p *principal) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id, name, slug, created_at FROM organizations o
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
	err := s.db.QueryRow(r.Context(), `INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id, name, slug, created_at`, in.Name, in.Slug).
		Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedAt)
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
	err := s.db.QueryRow(r.Context(), `SELECT id, name, slug, created_at FROM organizations WHERE id = $1`, orgID).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedAt)
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

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	type member struct {
		UserID      string `json:"userId"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Role        string `json:"role"`
		Status      string `json:"status"`
	}
	rows, err := s.db.Query(r.Context(), `SELECT u.id, u.email, u.display_name, m.role, u.status FROM organization_members m JOIN users u ON u.id = m.user_id WHERE m.org_id = $1 ORDER BY u.email`, orgID)
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
		Email string `json:"email"`
		Role  string `json:"role"`
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
	s.db.QueryRow(r.Context(), `SELECT role FROM organization_members WHERE org_id = $1 AND user_id = $2`, orgID, userID).Scan(&current)
	if current == "owner" && p.orgRole(orgID) != "owner" {
		forbidden(w)
		return
	}
	if _, err := s.db.Exec(r.Context(), `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, $3) ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role`, orgID, userID, in.Role); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "member.set_role", "user", userID, map[string]any{"role": in.Role, "previous": current})
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type orgRole struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"orgId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Scopes      []string  `json:"scopes"`
	MemberCount int       `json:"memberCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const orgRoleSelect = `
	SELECT r.id, r.org_id, r.name, r.description, r.scopes,
		(SELECT count(*) FROM organization_members m WHERE m.custom_role_id = r.id),
		r.created_at, r.updated_at
	FROM organization_roles r`

func scanOrgRole(row pgx.CollectableRow) (orgRole, error) {
	var r orgRole
	return r, row.Scan(&r.ID, &r.OrgID, &r.Name, &r.Description, &r.Scopes, &r.MemberCount, &r.CreatedAt, &r.UpdatedAt)
}

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permRolesRead, permMembersWrite)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), orgRoleSelect+` WHERE r.org_id = $1 ORDER BY lower(r.name)`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	roles, err := pgx.CollectRows(rows, scanOrgRole)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(roles))
}

type roleInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scopes      []string `json:"scopes"`
}

func (s *Server) decodeRoleInput(w http.ResponseWriter, r *http.Request) (roleInput, bool) {
	var in roleInput
	if !decode(w, r, 8<<10, &in) {
		return in, false
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 80 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Give the role a name of up to 80 characters.")
		return in, false
	}
	if utf8.RuneCountInString(in.Description) > 300 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_description", "Keep the description to 300 characters.")
		return in, false
	}
	scopes, msg := normalizeRoleScopes(in.Scopes)
	if msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_scopes", msg)
		return in, false
	}
	in.Scopes = scopes
	return in, true
}

func (s *Server) createRole(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.apiKey != nil {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "API keys cannot manage custom roles.")
		return
	}
	orgID, ok := s.orgPerm(w, r, p, permRolesWrite)
	if !ok {
		return
	}
	in, ok := s.decodeRoleInput(w, r)
	if !ok {
		return
	}
	var role orgRole
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO organization_roles (org_id, name, description, scopes) VALUES ($1, $2, $3, $4)
		RETURNING id, org_id, name, description, scopes, 0, created_at, updated_at`,
		orgID, in.Name, in.Description, in.Scopes).
		Scan(&role.ID, &role.OrgID, &role.Name, &role.Description, &role.Scopes, &role.MemberCount, &role.CreatedAt, &role.UpdatedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "role_name_taken", "Another custom role in this organization already uses this name.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "role.create", "role", role.ID, map[string]any{"name": role.Name, "scopes": role.Scopes})
	writeJSON(w, http.StatusCreated, role)
}

func (s *Server) updateRole(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.apiKey != nil {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "API keys cannot manage custom roles.")
		return
	}
	id, ok := pathID(w, r, "roleId")
	if !ok {
		return
	}
	var orgID string
	err := s.db.QueryRow(r.Context(), `SELECT org_id FROM organization_roles WHERE id = $1`, id).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.orgRole(orgID) == "") {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !p.can(orgID, permRolesWrite) {
		forbidden(w)
		return
	}
	in, ok := s.decodeRoleInput(w, r)
	if !ok {
		return
	}
	var role orgRole
	err = s.db.QueryRow(r.Context(), `
		UPDATE organization_roles SET name = $2, description = $3, scopes = $4, updated_at = now()
		WHERE id = $1
		RETURNING id, org_id, name, description, scopes,
			(SELECT count(*) FROM organization_members m WHERE m.custom_role_id = organization_roles.id),
			created_at, updated_at`,
		id, in.Name, in.Description, in.Scopes).
		Scan(&role.ID, &role.OrgID, &role.Name, &role.Description, &role.Scopes, &role.MemberCount, &role.CreatedAt, &role.UpdatedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "role_name_taken", "Another custom role in this organization already uses this name.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "role.update", "role", role.ID, map[string]any{"name": role.Name, "scopes": role.Scopes})
	writeJSON(w, http.StatusOK, role)
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.apiKey != nil {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "API keys cannot manage custom roles.")
		return
	}
	id, ok := pathID(w, r, "roleId")
	if !ok {
		return
	}
	var orgID, name string
	err := s.db.QueryRow(r.Context(), `SELECT org_id, name FROM organization_roles WHERE id = $1`, id).Scan(&orgID, &name)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.orgRole(orgID) == "") {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !p.can(orgID, permRolesWrite) {
		forbidden(w)
		return
	}
	var mapped bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM oidc_providers WHERE group_mappings @> jsonb_build_array(jsonb_build_object('customRoleId', $1::text)))`, id).Scan(&mapped); err != nil {
		s.internalError(w, r, err)
		return
	}
	if mapped {
		writeError(w, http.StatusConflict, "role_in_use", "An identity provider group mapping uses this role. Remove it from the provider first.")
		return
	}
	_, err = s.db.Exec(r.Context(), `DELETE FROM organization_roles WHERE id = $1`, id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		writeError(w, http.StatusConflict, "role_in_use", "One or more members still have this role. Reassign them before deleting it.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "role.delete", "role", id, map[string]any{"name": name})
	w.WriteHeader(http.StatusNoContent)
}

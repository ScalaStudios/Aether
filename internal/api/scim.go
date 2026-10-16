package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const (
	scimUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	scimMaxResults  = 200
)

var scimUserNameFilter = regexp.MustCompile(`(?i)^\s*userName\s+eq\s+"((?:[^"\\]|\\.)*)"\s*$`)

type scimEmail struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

type scimUser struct {
	Schemas    []string `json:"schemas"`
	ID         string   `json:"id,omitempty"`
	ExternalID string   `json:"externalId,omitempty"`
	UserName   string   `json:"userName"`
	Name       struct {
		Formatted string `json:"formatted,omitempty"`
	} `json:"name"`
	DisplayName string         `json:"displayName,omitempty"`
	Emails      []scimEmail    `json:"emails,omitempty"`
	Active      *bool          `json:"active,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

func scimError(w http.ResponseWriter, status int, scimType, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	body := map[string]any{"schemas": []string{scimErrorSchema}, "status": strconv.Itoa(status), "detail": detail}
	if scimType != "" {
		body["scimType"] = scimType
	}
	json.NewEncoder(w).Encode(body)
}

func scimJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) scimProvider(w http.ResponseWriter, r *http.Request) (*providerRow, bool) {
	p, err := scanProvider(s.db.QueryRow(r.Context(), `SELECT `+providerColumns+` FROM oidc_providers WHERE slug = $1 AND enabled`, r.PathValue("slug")))
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if err != nil || !ok || p.SCIMTokenHash == "" || subtle.ConstantTimeCompare([]byte(secrets.SHA256Hex(token)), []byte(p.SCIMTokenHash)) != 1 {
		scimError(w, http.StatusUnauthorized, "", "The bearer token is not valid for this provider's SCIM endpoint.")
		return nil, false
	}
	return p, true
}

func (s *Server) scimBase(p *providerRow) string {
	return strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/api/v1/scim/" + p.Slug
}

func (s *Server) scimServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.scimProvider(w, r); !ok {
		return
	}
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":               []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":                 map[string]bool{"supported": true},
		"bulk":                  map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":                map[string]any{"supported": true, "maxResults": scimMaxResults},
		"changePassword":        map[string]bool{"supported": false},
		"sort":                  map[string]bool{"supported": false},
		"etag":                  map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "Bearer token", "description": "The token shown when SCIM was turned on for this identity provider in Aether."}},
	})
}

type scimRow struct {
	ID, Email, DisplayName, Status, Subject string
}

func (s *Server) scimResource(p *providerRow, u scimRow) scimUser {
	out := scimUser{Schemas: []string{scimUserSchema}, ID: u.ID, ExternalID: u.Subject, UserName: u.Email, DisplayName: u.DisplayName}
	out.Name.Formatted = u.DisplayName
	out.Emails = []scimEmail{{u.Email, "work", true}}
	active := u.Status == "active"
	out.Active = &active
	out.Meta = map[string]any{"resourceType": "User", "location": s.scimBase(p) + "/Users/" + u.ID}
	return out
}

const scimRowColumns = `u.id, u.email, u.display_name, u.status, coalesce((SELECT subject FROM user_identities WHERE provider_id = $1 AND user_id = u.id LIMIT 1), '')`

func scanSCIMRow(row pgx.Row) (scimRow, error) {
	var u scimRow
	return u, row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Status, &u.Subject)
}

func (s *Server) scimListUsers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	var rows pgx.Rows
	var err error
	if filter := r.URL.Query().Get("filter"); filter != "" {
		m := scimUserNameFilter.FindStringSubmatch(filter)
		if m == nil {
			scimError(w, http.StatusBadRequest, "invalidFilter", `Only filters of the form userName eq "address" are supported.`)
			return
		}
		var userName string
		if err := json.Unmarshal([]byte(`"`+m[1]+`"`), &userName); err != nil {
			scimError(w, http.StatusBadRequest, "invalidFilter", "The userName in the filter is not a valid string.")
			return
		}
		rows, err = s.db.Query(r.Context(), `SELECT `+scimRowColumns+` FROM users u WHERE u.email = $2`, p.ID, strings.ToLower(userName))
	} else {
		rows, err = s.db.Query(r.Context(), `SELECT `+scimRowColumns+` FROM users u WHERE EXISTS (SELECT 1 FROM user_identities i WHERE i.provider_id = $1 AND i.user_id = u.id) ORDER BY u.email LIMIT $2`, p.ID, scimMaxResults)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (scimRow, error) { return scanSCIMRow(row) })
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resources := []scimUser{}
	for _, u := range users {
		resources = append(resources, s.scimResource(p, u))
	}
	scimJSON(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": len(resources), "startIndex": 1, "itemsPerPage": len(resources), "Resources": resources})
}

func (s *Server) scimUserFromPath(w http.ResponseWriter, r *http.Request, p *providerRow) (scimRow, bool) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		scimError(w, http.StatusNotFound, "", "No such user.")
		return scimRow{}, false
	}
	u, err := scanSCIMRow(s.db.QueryRow(r.Context(), `SELECT `+scimRowColumns+` FROM users u WHERE u.id = $2`, p.ID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		scimError(w, http.StatusNotFound, "", "No such user.")
		return scimRow{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return scimRow{}, false
	}
	return u, true
}

func (s *Server) scimGetUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	if u, ok := s.scimUserFromPath(w, r, p); ok {
		scimJSON(w, http.StatusOK, s.scimResource(p, u))
	}
}

func decodeSCIMUser(w http.ResponseWriter, r *http.Request) (*scimUser, string, bool) {
	var in scimUser
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "The request body is not a SCIM user.")
		return nil, "", false
	}
	email := strings.ToLower(strings.TrimSpace(in.UserName))
	for _, e := range in.Emails {
		if e.Primary && e.Value != "" {
			email = strings.ToLower(strings.TrimSpace(e.Value))
		}
	}
	if !strings.Contains(email, "@") || len(email) > 254 {
		scimError(w, http.StatusBadRequest, "invalidValue", "userName must be an email address.")
		return nil, "", false
	}
	return &in, email, true
}

func (in *scimUser) displayName() string {
	return firstNonEmpty(in.DisplayName, in.Name.Formatted)
}

func (s *Server) scimCreateUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	in, email, ok := decodeSCIMUser(w, r)
	if !ok {
		return
	}
	var userID string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)`, email).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errSCIMConflict
		}
		status := "active"
		if in.Active != nil && !*in.Active {
			status = "suspended"
		}
		err := tx.QueryRow(r.Context(), `INSERT INTO users (email, display_name, status, suspended_by) VALUES ($1, $2, $3, CASE WHEN $3 = 'active' THEN '' ELSE $4 END) RETURNING id`,
			email, in.displayName(), status, "provider:"+p.ID).Scan(&userID)
		if err != nil {
			return err
		}
		return s.scimLink(r.Context(), tx, p, userID, email, in.ExternalID)
	})
	if errors.Is(err, errSCIMConflict) {
		scimError(w, http.StatusConflict, "uniqueness", "A user with this userName already exists.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, nil, nil, "scim.user.create", "user", userID, map[string]any{"provider": p.Slug, "email": email})
	r.SetPathValue("id", userID)
	if u, ok := s.scimUserFromPath(w, r, p); ok {
		scimJSON(w, http.StatusCreated, s.scimResource(p, u))
	}
}

var errSCIMConflict = errors.New("scim conflict")

func (s *Server) scimLink(ctx context.Context, tx pgx.Tx, p *providerRow, userID, email, externalID string) error {
	if externalID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO user_identities (provider_id, subject, user_id) VALUES ($1, $2, $3) ON CONFLICT (provider_id, subject) DO NOTHING`, p.ID, externalID, userID); err != nil {
			return err
		}
	}
	return s.linkMailboxForIdentity(ctx, tx, userID, email, true)
}

func (s *Server) scimReplaceUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	u, ok := s.scimUserFromPath(w, r, p)
	if !ok {
		return
	}
	in, email, ok := decodeSCIMUser(w, r)
	if !ok {
		return
	}
	if email != u.Email {
		scimError(w, http.StatusBadRequest, "mutability", "Aether does not change a user's email address over SCIM. Change it in Aether.")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if name := in.displayName(); name != "" {
			if _, err := tx.Exec(r.Context(), `UPDATE users SET display_name = $2, updated_at = now() WHERE id = $1 AND display_name <> $2`, u.ID, name); err != nil {
				return err
			}
		}
		return s.scimLink(r.Context(), tx, p, u.ID, email, in.ExternalID)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if in.Active != nil {
		if err := s.scimSetActive(r.Context(), p, u.ID, *in.Active); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	s.audit(r.Context(), r, nil, nil, "scim.user.update", "user", u.ID, map[string]any{"provider": p.Slug})
	if u, ok := s.scimUserFromPath(w, r, p); ok {
		scimJSON(w, http.StatusOK, s.scimResource(p, u))
	}
}

func (s *Server) scimSetActive(ctx context.Context, p *providerRow, userID string, active bool) error {
	by := "provider:" + p.ID
	if !active {
		_, err := s.setUserStatus(ctx, userID, "suspended", by)
		return err
	}
	var suspendedBy string
	if err := s.db.QueryRow(ctx, `SELECT suspended_by FROM users WHERE id = $1`, userID).Scan(&suspendedBy); err != nil {
		return err
	}
	if suspendedBy != by {
		return nil
	}
	_, err := s.setUserStatus(ctx, userID, "active", by)
	return err
}

func scimBool(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		parsed, err := strconv.ParseBool(b)
		return parsed, err == nil
	}
	return false, false
}

func (s *Server) scimPatchUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	u, ok := s.scimUserFromPath(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Operations []struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value any    `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", "The request body is not a SCIM patch.")
		return
	}
	for _, op := range in.Operations {
		if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
			continue
		}
		values := map[string]any{}
		if op.Path != "" {
			values[op.Path] = op.Value
		} else if m, ok := op.Value.(map[string]any); ok {
			values = m
		}
		for path, value := range values {
			switch strings.ToLower(path) {
			case "active":
				active, ok := scimBool(value)
				if !ok {
					scimError(w, http.StatusBadRequest, "invalidValue", "active must be true or false.")
					return
				}
				if err := s.scimSetActive(r.Context(), p, u.ID, active); err != nil {
					s.internalError(w, r, err)
					return
				}
			case "displayname":
				if name, ok := value.(string); ok && strings.TrimSpace(name) != "" {
					if _, err := s.db.Exec(r.Context(), `UPDATE users SET display_name = $2, updated_at = now() WHERE id = $1`, u.ID, strings.TrimSpace(name)); err != nil {
						s.internalError(w, r, err)
						return
					}
				}
			}
		}
	}
	s.audit(r.Context(), r, nil, nil, "scim.user.update", "user", u.ID, map[string]any{"provider": p.Slug})
	if u, ok := s.scimUserFromPath(w, r, p); ok {
		scimJSON(w, http.StatusOK, s.scimResource(p, u))
	}
}

func (s *Server) scimDeleteUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.scimProvider(w, r)
	if !ok {
		return
	}
	u, ok := s.scimUserFromPath(w, r, p)
	if !ok {
		return
	}
	if _, err := s.setUserStatus(r.Context(), u.ID, "deprovisioned", "provider:"+p.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, nil, nil, "scim.user.deprovision", "user", u.ID, map[string]any{"provider": p.Slug})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) enableSCIM(w http.ResponseWriter, r *http.Request, pr *principal) {
	if !s.requirePlatformAdmin(w, pr) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	token := "scim_" + secrets.Token(32)
	var slug string
	err := s.db.QueryRow(r.Context(), `UPDATE oidc_providers SET scim_token_hash = $2, updated_at = now() WHERE id = $1 RETURNING slug`, id, secrets.SHA256Hex(token)).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, pr, nil, "identity_provider.scim_token", "identity_provider", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"url": strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/api/v1/scim/" + slug, "token": token})
}

func (s *Server) disableSCIM(w http.ResponseWriter, r *http.Request, pr *principal) {
	if !s.requirePlatformAdmin(w, pr) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE oidc_providers SET scim_token_hash = '', updated_at = now() WHERE id = $1`, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, pr, nil, "identity_provider.scim_disable", "identity_provider", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

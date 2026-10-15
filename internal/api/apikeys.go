package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/secrets"
)

const apiKeyPrefix = "aether_"

var apiKeyResources = []string{"domains", "mailboxes", "aliases", "members", "audit", "webhooks"}

var apiKeyRoutes = map[string]string{
	"GET /api/v1/organizations/{orgId}":            "",
	"GET /api/v1/organizations/{orgId}/members":    "members",
	"POST /api/v1/organizations/{orgId}/members":   "members",
	"GET /api/v1/organizations/{orgId}/audit":      "audit",
	"GET /api/v1/organizations/{orgId}/domains":    "domains",
	"POST /api/v1/organizations/{orgId}/domains":   "domains",
	"GET /api/v1/domains/{domainId}":               "domains",
	"DELETE /api/v1/domains/{domainId}":            "domains",
	"GET /api/v1/domains/{domainId}/dns":           "domains",
	"POST /api/v1/domains/{domainId}/verify":       "domains",
	"GET /api/v1/domains/{domainId}/dmarc":         "domains",
	"GET /api/v1/organizations/{orgId}/mailboxes":  "mailboxes",
	"POST /api/v1/organizations/{orgId}/mailboxes": "mailboxes",
	"PATCH /api/v1/mailboxes/{mailboxId}":          "mailboxes",
	"DELETE /api/v1/mailboxes/{mailboxId}":         "mailboxes",
	"GET /api/v1/organizations/{orgId}/aliases":    "aliases",
	"POST /api/v1/organizations/{orgId}/aliases":   "aliases",
	"DELETE /api/v1/aliases/{aliasId}":             "aliases",
	"GET /api/v1/organizations/{orgId}/webhooks":   "webhooks",
	"POST /api/v1/organizations/{orgId}/webhooks":  "webhooks",
	"DELETE /api/v1/webhooks/{webhookId}":          "webhooks",
	"GET /api/v1/webhooks/{webhookId}/deliveries":  "webhooks",
	"POST /api/v1/webhooks/{webhookId}/test":       "webhooks",
}

type apiKeyIdentity struct {
	ID     string
	Name   string
	Scopes []string
}

func (k *apiKeyIdentity) permits(method, resource string) bool {
	if resource == "" {
		return true
	}
	if slices.Contains(k.Scopes, resource+":write") {
		return true
	}
	return (method == http.MethodGet || method == http.MethodHead) && slices.Contains(k.Scopes, resource+":read")
}

func (s *Server) apiKeyPrincipal(w http.ResponseWriter, r *http.Request, token string) (*principal, bool) {
	resource, routable := apiKeyRoutes[r.Pattern]
	if !routable {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "API keys cannot use this endpoint.")
		return nil, false
	}
	key := &apiKeyIdentity{}
	var orgID string
	err := s.db.QueryRow(r.Context(), `
		UPDATE api_keys SET last_used_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		RETURNING id, name, scopes, org_id`, secrets.SHA256(token)).Scan(&key.ID, &key.Name, &key.Scopes, &orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "The API key is not valid.")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	if !s.allow(r.Context(), "apikey:"+key.ID, 600) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "This API key is making too many requests. Wait a minute.")
		return nil, false
	}
	if !key.permits(r.Method, resource) {
		writeError(w, http.StatusForbidden, "insufficient_scope", "This API key does not have the "+resource+" scope needed for this request.")
		return nil, false
	}
	return &principal{apiKey: key, roles: map[string]string{orgID: "admin"}}, true
}

type apiKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
}

const apiKeyColumns = `id, name, prefix, scopes, created_at, last_used_at, expires_at`

func scanAPIKey(row pgx.CollectableRow) (apiKey, error) {
	var k apiKey
	return k, row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Scopes, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt)
}

func (s *Server) sessionOrgAdmin(w http.ResponseWriter, r *http.Request, p *principal) (string, bool) {
	orgID, ok := pathID(w, r, "orgId")
	if !ok {
		return "", false
	}
	if p.apiKey != nil {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "API keys cannot manage API keys.")
		return "", false
	}
	if !p.canAdminOrg(orgID) {
		notFound(w)
		return "", false
	}
	return orgID, true
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.sessionOrgAdmin(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+apiKeyColumns+` FROM api_keys WHERE org_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	keys, err := pgx.CollectRows(rows, scanAPIKey)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.sessionOrgAdmin(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Give the key a name of up to 100 characters.")
		return
	}
	if len(in.Scopes) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_scopes", "Choose at least one scope.")
		return
	}
	for _, scope := range in.Scopes {
		resource, access, _ := strings.Cut(scope, ":")
		if !slices.Contains(apiKeyResources, resource) || (access != "read" && access != "write") {
			writeError(w, http.StatusUnprocessableEntity, "invalid_scopes", scope+" is not a valid scope.")
			return
		}
	}
	if in.ExpiresAt != nil && in.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_expiry", "The expiry date must be in the future.")
		return
	}
	token := apiKeyPrefix + secrets.Token(30)
	var k apiKey
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO api_keys (org_id, name, prefix, token_hash, scopes, created_by, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+apiKeyColumns, orgID, in.Name, token[:len(apiKeyPrefix)+6], secrets.SHA256(token), in.Scopes, p.User.ID, in.ExpiresAt).
		Scan(&k.ID, &k.Name, &k.Prefix, &k.Scopes, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "api_key.create", "api_key", k.ID, map[string]any{"name": k.Name, "scopes": k.Scopes})
	writeJSON(w, http.StatusCreated, map[string]any{"key": k, "token": token})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "keyId")
	if !ok {
		return
	}
	if p.apiKey != nil {
		writeError(w, http.StatusForbidden, "api_key_not_allowed", "API keys cannot manage API keys.")
		return
	}
	var orgID string
	if err := s.db.QueryRow(r.Context(), `SELECT org_id FROM api_keys WHERE id = $1 AND revoked_at IS NULL`, id).Scan(&orgID); err != nil || !p.canAdminOrg(orgID) {
		notFound(w)
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE api_keys SET revoked_at = now() WHERE id = $1`, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "api_key.revoke", "api_key", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

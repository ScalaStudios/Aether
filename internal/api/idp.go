package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type identityProvider struct {
	ID              string         `json:"id"`
	Slug            string         `json:"slug"`
	Name            string         `json:"name"`
	Kind            string         `json:"kind"`
	Issuer          string         `json:"issuer"`
	ClientID        string         `json:"clientId"`
	HasClientSecret bool           `json:"hasClientSecret"`
	Scopes          []string       `json:"scopes"`
	AutoProvision   bool           `json:"autoProvision"`
	AdminGroup      string         `json:"adminGroup"`
	GroupMappings   []groupMapping `json:"groupMappings"`
	Enabled         bool           `json:"enabled"`
	RedirectURI     string         `json:"redirectUri"`
	HasWebhook      bool           `json:"hasWebhookSecret"`
	WebhookURL      string         `json:"webhookUrl"`
}

type identityProviderInput struct {
	Slug          string         `json:"slug"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	Issuer        string         `json:"issuer"`
	ClientID      string         `json:"clientId"`
	ClientSecret  *string        `json:"clientSecret"`
	WebhookSecret *string        `json:"webhookSecret"`
	Scopes        []string       `json:"scopes"`
	AutoProvision bool           `json:"autoProvision"`
	AdminGroup    string         `json:"adminGroup"`
	GroupMappings []groupMapping `json:"groupMappings"`
	Enabled       *bool          `json:"enabled"`
}

func (s *Server) toIdentityProvider(p *providerRow) identityProvider {
	return identityProvider{
		ID: p.ID, Slug: p.Slug, Name: p.Name, Kind: p.Kind, Issuer: p.Issuer, ClientID: p.ClientID,
		HasClientSecret: len(p.ClientSecret) > 0, Scopes: p.Scopes, AutoProvision: p.AutoProvision,
		AdminGroup: p.AdminGroup, GroupMappings: nonNil(p.GroupMappings), Enabled: p.Enabled, RedirectURI: s.redirectURI(p.Slug),
		HasWebhook: len(p.WebhookSecret) > 0, WebhookURL: strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/api/v1/hooks/identity-providers/" + p.Slug,
	}
}

func (s *Server) requirePlatformAdmin(w http.ResponseWriter, p *principal) bool {
	if !p.User.IsPlatformAdmin {
		forbidden(w)
		return false
	}
	return true
}

func (s *Server) listIdentityProviders(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+providerColumns+` FROM oidc_providers ORDER BY name`)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	out := []identityProvider{}
	for rows.Next() {
		row, err := scanProvider(rows)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out = append(out, s.toIdentityProvider(row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) validIssuer(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && s.cfg.AllowHTTPIssuers)
}

func (s *Server) validateProviderInput(w http.ResponseWriter, r *http.Request, in *identityProviderInput) bool {
	in.Issuer = strings.TrimRight(strings.TrimSpace(in.Issuer), "/")
	if in.Kind == "" {
		in.Kind = "generic"
	}
	if len(in.Scopes) == 0 {
		in.Scopes = []string{"openid", "profile", "email"}
		if in.Kind == "halo" {
			in.Scopes = append(in.Scopes, "groups")
		}
	}
	switch {
	case !slugPattern.MatchString(in.Slug):
		writeError(w, http.StatusUnprocessableEntity, "invalid_slug", "Use lowercase letters, digits, and hyphens for the slug.")
	case strings.TrimSpace(in.Name) == "":
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Enter a display name for the provider.")
	case in.Kind != "halo" && in.Kind != "generic":
		writeError(w, http.StatusUnprocessableEntity, "invalid_kind", "Kind must be halo or generic.")
	case !s.validIssuer(in.Issuer):
		writeError(w, http.StatusUnprocessableEntity, "invalid_issuer", "The issuer must be an https URL without a query string.")
	case strings.TrimSpace(in.ClientID) == "":
		writeError(w, http.StatusUnprocessableEntity, "invalid_client", "Enter the client ID issued by the provider.")
	case !slices.Contains(in.Scopes, "openid"):
		writeError(w, http.StatusUnprocessableEntity, "invalid_scopes", "Scopes must include openid.")
	default:
		for _, m := range in.GroupMappings {
			if m.Group == "" || !uuidPattern.MatchString(m.OrgID) || (m.Role != "owner" && m.Role != "admin" && m.Role != "member") {
				writeError(w, http.StatusUnprocessableEntity, "invalid_group_mapping", "Each group mapping needs a group name, an organization, and a role of owner, admin, or member.")
				return false
			}
		}
		return true
	}
	return false
}

func (s *Server) createIdentityProvider(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	var in identityProviderInput
	if !decode(w, r, 64<<10, &in) || !s.validateProviderInput(w, r, &in) {
		return
	}
	s.saveIdentityProvider(w, r, p, "", &in)
}

func (s *Server) updateIdentityProvider(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in identityProviderInput
	if !decode(w, r, 64<<10, &in) || !s.validateProviderInput(w, r, &in) {
		return
	}
	s.saveIdentityProvider(w, r, p, id, &in)
}

func (s *Server) saveIdentityProvider(w http.ResponseWriter, r *http.Request, p *principal, id string, in *identityProviderInput) {
	mappings, _ := json.Marshal(nonNil(in.GroupMappings))
	enabled := in.Enabled == nil || *in.Enabled
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if id == "" {
			err := tx.QueryRow(r.Context(), `INSERT INTO oidc_providers (slug, name, kind, issuer, client_id, scopes, auto_provision, admin_group, group_mappings, enabled) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
				in.Slug, in.Name, in.Kind, in.Issuer, in.ClientID, in.Scopes, in.AutoProvision, in.AdminGroup, mappings, enabled).Scan(&id)
			if err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(r.Context(), `UPDATE oidc_providers SET slug = $2, name = $3, kind = $4, issuer = $5, client_id = $6, scopes = $7, auto_provision = $8, admin_group = $9, group_mappings = $10, enabled = $11, updated_at = now() WHERE id = $1`,
				id, in.Slug, in.Name, in.Kind, in.Issuer, in.ClientID, in.Scopes, in.AutoProvision, in.AdminGroup, mappings, enabled)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return pgx.ErrNoRows
			}
		}
		if in.WebhookSecret != nil {
			var sealed []byte
			if *in.WebhookSecret != "" {
				sealed = s.box.Seal([]byte(*in.WebhookSecret), "oidc-webhook:"+id)
			}
			if _, err := tx.Exec(r.Context(), `UPDATE oidc_providers SET webhook_secret = $2 WHERE id = $1`, id, sealed); err != nil {
				return err
			}
		}
		if in.ClientSecret != nil {
			var sealed []byte
			if *in.ClientSecret != "" {
				sealed = s.box.Seal([]byte(*in.ClientSecret), "oidc:"+id)
			}
			_, err := tx.Exec(r.Context(), `UPDATE oidc_providers SET client_secret = $2, updated_at = now() WHERE id = $1`, id, sealed)
			return err
		}
		return nil
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w)
		return
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		writeError(w, http.StatusConflict, "slug_taken", "Another identity provider already uses this slug.")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	row, err := scanProvider(s.db.QueryRow(r.Context(), `SELECT `+providerColumns+` FROM oidc_providers WHERE id = $1`, id))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	status := http.StatusOK
	action := "identity_provider.update"
	if r.Method == http.MethodPost {
		status, action = http.StatusCreated, "identity_provider.create"
	}
	s.audit(r.Context(), r, p, nil, action, "identity_provider", id, map[string]any{"slug": in.Slug, "issuer": in.Issuer, "secretChanged": in.ClientSecret != nil, "webhookSecretChanged": in.WebhookSecret != nil})
	writeJSON(w, status, s.toIdentityProvider(row))
}

func (s *Server) deleteIdentityProvider(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM oidc_providers WHERE id = $1`, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, nil, "identity_provider.delete", "identity_provider", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) discoverIdentityProvider(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	var in struct {
		Issuer string `json:"issuer"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	issuer := strings.TrimRight(strings.TrimSpace(in.Issuer), "/")
	if !s.validIssuer(issuer) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_issuer", "The issuer must be an https URL without a query string.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "discovery_failed", "Could not load "+issuer+"/.well-known/openid-configuration: "+err.Error())
		return
	}
	var meta discovered
	provider.Claims(&meta)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                issuer,
		"authorizationEndpoint": provider.Endpoint().AuthURL,
		"tokenEndpoint":         provider.Endpoint().TokenURL,
		"userinfoEndpoint":      meta.Userinfo,
		"endSessionEndpoint":    meta.EndSession,
		"scopesSupported":       nonNil(meta.ScopesSupported),
		"pkceS256":              slices.Contains(meta.CodeChallengeMethods, "S256"),
		"groupsClaim":           slices.Contains(meta.ClaimsSupported, "groups") || slices.Contains(meta.ScopesSupported, "groups"),
		"redirectUri":           s.redirectURI("{slug}"),
	})
}

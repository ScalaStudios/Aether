package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/scalastudios/aether/internal/secrets"
)

const stateCookie = "aether_oidc_state"

type providerRow struct {
	ID            string
	Slug          string
	Name          string
	Kind          string
	Issuer        string
	ClientID      string
	ClientSecret  []byte
	WebhookSecret []byte
	Scopes        []string
	AutoProvision bool
	AdminGroup    string
	GroupMappings []groupMapping
	Enabled       bool
	UpdatedAt     time.Time
}

type groupMapping struct {
	Group string `json:"group"`
	OrgID string `json:"orgId"`
	Role  string `json:"role"`
}

type discovered struct {
	EndSession           string   `json:"end_session_endpoint"`
	Userinfo             string   `json:"userinfo_endpoint"`
	CodeChallengeMethods []string `json:"code_challenge_methods_supported"`
	ScopesSupported      []string `json:"scopes_supported"`
	ClaimsSupported      []string `json:"claims_supported"`
}

type oidcClient struct {
	updatedAt time.Time
	provider  *oidc.Provider
	oauth     oauth2.Config
	verifier  *oidc.IDTokenVerifier
	meta      discovered
}

type oidcCache struct {
	mu      sync.Mutex
	clients map[string]*oidcClient
}

func newOIDCCache() *oidcCache {
	return &oidcCache{clients: map[string]*oidcClient{}}
}

const providerColumns = `id, slug, name, kind, issuer, client_id, client_secret, webhook_secret, scopes, auto_provision, admin_group, group_mappings, enabled, updated_at`

func scanProvider(row pgx.Row) (*providerRow, error) {
	p := &providerRow{}
	var mappings []byte
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Kind, &p.Issuer, &p.ClientID, &p.ClientSecret, &p.WebhookSecret, &p.Scopes, &p.AutoProvision, &p.AdminGroup, &mappings, &p.Enabled, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, json.Unmarshal(mappings, &p.GroupMappings)
}

func (s *Server) redirectURI(slug string) string {
	return strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/api/v1/auth/oidc/" + slug + "/callback"
}

func (s *Server) oidcClientFor(ctx context.Context, p *providerRow) (*oidcClient, error) {
	s.oidc.mu.Lock()
	cached := s.oidc.clients[p.ID]
	s.oidc.mu.Unlock()
	if cached != nil && cached.updatedAt.Equal(p.UpdatedAt) {
		return cached, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, p.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovery for %s: %w", p.Issuer, err)
	}
	c := &oidcClient{updatedAt: p.UpdatedAt, provider: provider}
	if err := provider.Claims(&c.meta); err != nil {
		return nil, err
	}
	var secret string
	if len(p.ClientSecret) > 0 {
		plain, err := s.box.Open(p.ClientSecret, "oidc:"+p.ID)
		if err != nil {
			return nil, fmt.Errorf("decrypt client secret: %w", err)
		}
		secret = string(plain)
	}
	c.oauth = oauth2.Config{ClientID: p.ClientID, ClientSecret: secret, Endpoint: provider.Endpoint(), RedirectURL: s.redirectURI(p.Slug), Scopes: p.Scopes}
	c.verifier = provider.Verifier(&oidc.Config{ClientID: p.ClientID})
	s.oidc.mu.Lock()
	s.oidc.clients[p.ID] = c
	s.oidc.mu.Unlock()
	return c, nil
}

func (s *Server) loginRedirect(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(code), http.StatusFound)
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if !s.allow(r.Context(), "oidc-ip:"+s.clientIP(r), 30) {
		s.loginRedirect(w, r, "rate_limited")
		return
	}
	p, err := scanProvider(s.db.QueryRow(r.Context(), `SELECT `+providerColumns+` FROM oidc_providers WHERE slug = $1 AND enabled`, r.PathValue("slug")))
	if errors.Is(err, pgx.ErrNoRows) {
		s.loginRedirect(w, r, "unknown_provider")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	client, err := s.oidcClientFor(r.Context(), p)
	if err != nil {
		s.log.Error("oidc provider unavailable", "provider", p.Slug, "err", err)
		s.loginRedirect(w, r, "provider_unavailable")
		return
	}
	state := secrets.Token(32)
	verifier := oauth2.GenerateVerifier()
	nonce := secrets.Token(16)
	_, err = s.db.Exec(r.Context(), `INSERT INTO oidc_login_states (state_hash, provider_id, code_verifier, nonce, return_to, expires_at) VALUES ($1, $2, $3, $4, $5, now() + interval '10 minutes')`,
		secrets.SHA256(state), p.ID, verifier, nonce, safeReturnTo(r.URL.Query().Get("return_to")))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.db.Exec(r.Context(), `DELETE FROM oidc_login_states WHERE expires_at < now()`)
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: state, Path: "/api/v1/auth/oidc/", MaxAge: 600, HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, client.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), http.StatusFound)
}

type idClaims struct {
	Subject           string   `json:"sub"`
	Email             string   `json:"email"`
	EmailVerified     *bool    `json:"email_verified"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("error") != "" {
		s.loginRedirect(w, r, "provider_denied")
		return
	}
	c, err := r.Cookie(stateCookie)
	if err != nil || !constantTimeEqual(c.Value, q.Get("state")) {
		s.loginRedirect(w, r, "state_mismatch")
		return
	}
	clearCookie(w, stateCookie, s.cfg.SecureCookies())
	var providerID, verifier, nonce, returnTo string
	err = s.db.QueryRow(r.Context(), `DELETE FROM oidc_login_states WHERE state_hash = $1 AND expires_at > now() RETURNING provider_id, code_verifier, nonce, return_to`,
		secrets.SHA256(q.Get("state"))).Scan(&providerID, &verifier, &nonce, &returnTo)
	if errors.Is(err, pgx.ErrNoRows) {
		s.loginRedirect(w, r, "state_expired")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	p, err := scanProvider(s.db.QueryRow(r.Context(), `SELECT `+providerColumns+` FROM oidc_providers WHERE id = $1 AND enabled AND slug = $2`, providerID, r.PathValue("slug")))
	if err != nil {
		s.loginRedirect(w, r, "unknown_provider")
		return
	}
	client, err := s.oidcClientFor(r.Context(), p)
	if err != nil {
		s.log.Error("oidc provider unavailable", "provider", p.Slug, "err", err)
		s.loginRedirect(w, r, "provider_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := client.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		s.log.Warn("oidc code exchange failed", "provider", p.Slug, "err", err)
		s.loginRedirect(w, r, "exchange_failed")
		return
	}
	rawID, _ := token.Extra("id_token").(string)
	idToken, err := client.verifier.Verify(ctx, rawID)
	if err != nil || idToken.Nonce != nonce {
		s.log.Warn("oidc id token rejected", "provider", p.Slug, "err", err)
		s.loginRedirect(w, r, "invalid_token")
		return
	}
	var claims idClaims
	if err := idToken.Claims(&claims); err != nil {
		s.loginRedirect(w, r, "invalid_token")
		return
	}
	if client.meta.Userinfo != "" && (claims.Email == "" || (claims.Groups == nil && slices.Contains(p.Scopes, "groups"))) {
		if info, err := client.provider.UserInfo(ctx, oauth2.StaticTokenSource(token)); err == nil && info.Subject == claims.Subject {
			info.Claims(&claims)
		}
	}
	userID, code, err := s.resolveOIDCUser(r.Context(), p, &claims)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if code != "" {
		authAttempts.WithLabelValues("oidc", "failure").Inc()
		s.audit(r.Context(), r, nil, nil, "auth.oidc.rejected", "identity", p.Slug+":"+claims.Subject, map[string]any{"reason": code})
		s.loginRedirect(w, r, code)
		return
	}
	if _, err := s.startSession(r.Context(), w, r, userID, &p.ID, &rawID, oidcSessionTTL); err != nil {
		s.internalError(w, r, err)
		return
	}
	authAttempts.WithLabelValues("oidc", "success").Inc()
	s.audit(r.Context(), r, &principal{User: User{ID: userID}}, nil, "auth.login", "user", userID, map[string]any{"method": "oidc", "provider": p.Slug})
	http.Redirect(w, r, safeReturnTo(returnTo), http.StatusFound)
}

func (s *Server) resolveOIDCUser(ctx context.Context, p *providerRow, c *idClaims) (string, string, error) {
	if c.Subject == "" {
		return "", "invalid_token", nil
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	verified := c.EmailVerified != nil && *c.EmailVerified
	var userID, status string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT u.id, u.status FROM user_identities i JOIN users u ON u.id = i.user_id WHERE i.provider_id = $1 AND i.subject = $2`, p.ID, c.Subject).Scan(&userID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			if email == "" || !verified {
				return nil
			}
			err = tx.QueryRow(ctx, `SELECT id, status FROM users WHERE email = $1`, email).Scan(&userID, &status)
			if errors.Is(err, pgx.ErrNoRows) {
				if !p.AutoProvision {
					return nil
				}
				status = "active"
				err = tx.QueryRow(ctx, `INSERT INTO users (email, display_name) VALUES ($1, $2) RETURNING id`, email, firstNonEmpty(c.Name, c.PreferredUsername)).Scan(&userID)
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_identities (provider_id, subject, user_id) VALUES ($1, $2, $3)`, p.ID, c.Subject, userID); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if userID == "" || status != "active" {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE user_identities SET last_login_at = now() WHERE provider_id = $1 AND subject = $2`, p.ID, c.Subject); err != nil {
			return err
		}
		if c.Name != "" {
			if _, err := tx.Exec(ctx, `UPDATE users SET display_name = $2, updated_at = now() WHERE id = $1 AND display_name <> $2`, userID, c.Name); err != nil {
				return err
			}
		}
		if p.AdminGroup != "" && slices.Contains(c.Groups, p.AdminGroup) {
			if _, err := tx.Exec(ctx, `UPDATE users SET is_platform_admin = true WHERE id = $1`, userID); err != nil {
				return err
			}
		}
		for _, m := range p.GroupMappings {
			if !slices.Contains(c.Groups, m.Group) {
				continue
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, $3)
				ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role
				WHERE array_position(ARRAY['member','admin','owner'], organization_members.role) < array_position(ARRAY['member','admin','owner'], EXCLUDED.role)`,
				m.OrgID, userID, m.Role)
			if err != nil {
				return err
			}
		}
		if verified && email != "" {
			return s.linkMailboxForIdentity(ctx, tx, userID, email, p.AutoProvision)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	switch {
	case userID == "" && (email == "" || !verified):
		return "", "email_unverified", nil
	case userID == "":
		return "", "not_provisioned", nil
	case status != "active":
		return "", "account_disabled", nil
	}
	return userID, "", nil
}

func (s *Server) linkMailboxForIdentity(ctx context.Context, tx pgx.Tx, userID, email string, provision bool) error {
	var mailboxID, orgID string
	var owner *string
	err := tx.QueryRow(ctx, `SELECT id, org_id, owner_user_id FROM mailboxes WHERE address = $1`, email).Scan(&mailboxID, &orgID, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		if !provision {
			return nil
		}
		local, domain, _ := strings.Cut(email, "@")
		var domainID string
		err = tx.QueryRow(ctx, `SELECT id, org_id FROM domains WHERE name = $1 AND active AND verified_at IS NOT NULL`, domain).Scan(&domainID, &orgID)
		if errors.Is(err, pgx.ErrNoRows) || !localPartPattern.MatchString(local) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mailboxes (org_id, domain_id, local_part, address, owner_user_id) VALUES ($1, $2, $3, $4, $5)`, orgID, domainID, local, email, userID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if owner == nil {
		if _, err := tx.Exec(ctx, `UPDATE mailboxes SET owner_user_id = $2 WHERE id = $1`, mailboxID, userID); err != nil {
			return err
		}
	} else if *owner != userID {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT DO NOTHING`, orgID, userID)
	return err
}

func (s *Server) endSessionURL(ctx context.Context, providerID string, idToken *string) string {
	p, err := scanProvider(s.db.QueryRow(ctx, `SELECT `+providerColumns+` FROM oidc_providers WHERE id = $1`, providerID))
	if err != nil {
		return ""
	}
	client, err := s.oidcClientFor(ctx, p)
	if err != nil || client.meta.EndSession == "" {
		return ""
	}
	u, err := url.Parse(client.meta.EndSession)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", p.ClientID)
	q.Set("post_logout_redirect_uri", strings.TrimRight(s.cfg.PublicURL.String(), "/")+"/login")
	if idToken != nil {
		q.Set("id_token_hint", *idToken)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

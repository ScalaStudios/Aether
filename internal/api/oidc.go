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
	SCIMTokenHash string
}

type groupMapping struct {
	Group        string   `json:"group"`
	OrgID        string   `json:"orgId"`
	Role         string   `json:"role"`
	CustomRoleID string   `json:"customRoleId,omitempty"`
	DomainIDs    []string `json:"domainIds,omitempty"`
}

type discovered struct {
	EndSession           string   `json:"end_session_endpoint"`
	Userinfo             string   `json:"userinfo_endpoint"`
	Introspection        string   `json:"introspection_endpoint"`
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

const providerColumns = `id, slug, name, kind, issuer, client_id, client_secret, webhook_secret, scopes, auto_provision, admin_group, group_mappings, enabled, updated_at, scim_token_hash`

func scanProvider(row pgx.Row) (*providerRow, error) {
	p := &providerRow{}
	var mappings []byte
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Kind, &p.Issuer, &p.ClientID, &p.ClientSecret, &p.WebhookSecret, &p.Scopes, &p.AutoProvision, &p.AdminGroup, &mappings, &p.Enabled, &p.UpdatedAt, &p.SCIMTokenHash)
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
	Picture           string   `json:"picture"`
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
	if client.meta.Userinfo != "" && (claims.Email == "" || claims.Picture == "" || (claims.Groups == nil && slices.Contains(p.Scopes, "groups"))) {
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
		verified := claims.EmailVerified != nil && *claims.EmailVerified
		orgID := s.mailboxOrgForOIDC(r.Context(), p.ID, claims.Subject, claims.Email, verified)
		s.audit(r.Context(), r, nil, orgID, "auth.oidc.rejected", "identity", p.Slug+":"+claims.Subject, map[string]any{"reason": code, "provider": p.Slug})
		s.loginRedirect(w, r, code)
		return
	}
	_, sessionHash, err := s.startSession(r.Context(), w, r, userID, &p.ID, &rawID, oidcSessionTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	go s.syncProviderAvatar(userID, p.Issuer, claims.Picture, token)
	authAttempts.WithLabelValues("oidc", "success").Inc()
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	orgID := s.mailboxOrgForAccount(r.Context(), userID, email)
	s.audit(r.Context(), r, &principal{User: User{ID: userID, Email: email}}, orgID, "auth.login", "user", userID, map[string]any{"method": "oidc", "provider": p.Slug})
	s.checkSignInNotification(r.Context(), r, userID, "oidc", p.Slug, sessionHash)
	http.Redirect(w, r, safeReturnTo(returnTo), http.StatusFound)
}

func (s *Server) resolveOIDCUser(ctx context.Context, p *providerRow, c *idClaims) (string, string, error) {
	if c.Subject == "" {
		return "", "invalid_token", nil
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	verified := c.EmailVerified != nil && *c.EmailVerified
	var userID, status string
	var created bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT u.id, u.status FROM user_identities i JOIN users u ON u.id = i.user_id WHERE i.provider_id = $1 AND i.subject = $2`, p.ID, c.Subject).Scan(&userID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			if email == "" || !verified {
				return nil
			}
			err = tx.QueryRow(ctx, `SELECT id, status FROM users WHERE email = $1`, email).Scan(&userID, &status)
			if errors.Is(err, pgx.ErrNoRows) {
				if !p.AutoProvision || strings.ContainsAny(email, "*?") {
					return nil
				}
				status, created = "active", true
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
		if err := s.applyGroupMappings(ctx, tx, userID, c.Groups, p.GroupMappings); err != nil {
			return err
		}
		if verified && email != "" {
			err := s.linkMailboxForIdentity(ctx, tx, userID, email, p.AutoProvision)
			if created {
				return err
			}
			return ignoreLimit(err)
		}
		return nil
	})
	if errors.Is(err, errSharedAddress) {
		return "", "shared_mailbox", nil
	}
	var limit *limitError
	if errors.As(err, &limit) {
		return "", "limit_reached", nil
	}
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

type resolvedMapping struct {
	OrgID        string
	Role         string
	CustomRoleID *string
	DomainIDs    []string
	OrgWide      bool
}

func resolveMappedRoles(groups []string, mappings []groupMapping) []resolvedMapping {
	order := []string{}
	byOrg := map[string]resolvedMapping{}
	for _, m := range mappings {
		if !slices.Contains(groups, m.Group) || !isValidOrgRole(m.Role) || !uuidPattern.MatchString(m.OrgID) {
			continue
		}
		if m.Role == roleCustom && !uuidPattern.MatchString(m.CustomRoleID) {
			continue
		}
		cand := resolvedMapping{OrgID: m.OrgID, Role: m.Role}
		if m.Role == roleCustom {
			id := strings.ToLower(m.CustomRoleID)
			cand.CustomRoleID = &id
		}
		if m.Role == roleDomainAdmin || (m.Role == roleCustom && len(m.DomainIDs) > 0) {
			for _, d := range m.DomainIDs {
				d = strings.ToLower(strings.TrimSpace(d))
				if uuidPattern.MatchString(d) && !slices.Contains(cand.DomainIDs, d) {
					cand.DomainIDs = append(cand.DomainIDs, d)
				}
			}
		} else {
			cand.OrgWide = true
		}
		cur, exists := byOrg[m.OrgID]
		if !exists {
			order = append(order, m.OrgID)
			byOrg[m.OrgID] = cand
			continue
		}
		if roleRank(cand.Role) > roleRank(cur.Role) {
			byOrg[m.OrgID] = cand
			continue
		}
		if cand.Role != cur.Role {
			continue
		}
		if cand.Role == roleDomainAdmin {
			for _, d := range cand.DomainIDs {
				if !slices.Contains(cur.DomainIDs, d) {
					cur.DomainIDs = append(cur.DomainIDs, d)
				}
			}
			byOrg[m.OrgID] = cur
		} else if cand.Role == roleCustom && cur.CustomRoleID != nil && cand.CustomRoleID != nil && *cur.CustomRoleID == *cand.CustomRoleID {
			if cand.OrgWide || cur.OrgWide {
				cur.OrgWide = true
				cur.DomainIDs = nil
			} else {
				for _, d := range cand.DomainIDs {
					if !slices.Contains(cur.DomainIDs, d) {
						cur.DomainIDs = append(cur.DomainIDs, d)
					}
				}
			}
			byOrg[m.OrgID] = cur
		}
	}
	out := make([]resolvedMapping, 0, len(order))
	for _, orgID := range order {
		out = append(out, byOrg[orgID])
	}
	return out
}

func (s *Server) applyGroupMappings(ctx context.Context, tx pgx.Tx, userID string, groups []string, mappings []groupMapping) error {
	for _, rm := range resolveMappedRoles(groups, mappings) {
		var orgExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organizations WHERE id = $1)`, rm.OrgID).Scan(&orgExists); err != nil {
			return err
		}
		if !orgExists {
			continue
		}
		if rm.Role == roleCustom {
			var roleExists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organization_roles WHERE id = $1 AND org_id = $2)`, *rm.CustomRoleID, rm.OrgID).Scan(&roleExists); err != nil {
				return err
			}
			if !roleExists {
				continue
			}
		}
		validDomains := []string{}
		if len(rm.DomainIDs) > 0 {
			rows, err := tx.Query(ctx, `SELECT id::text FROM domains WHERE org_id = $1 AND id::text = ANY($2)`, rm.OrgID, rm.DomainIDs)
			if err != nil {
				return err
			}
			validDomains, err = pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
		}
		var existingRole string
		var existingCustomID *string
		err := tx.QueryRow(ctx, `SELECT role, custom_role_id FROM organization_members WHERE org_id = $1 AND user_id = $2`, rm.OrgID, userID).Scan(&existingRole, &existingCustomID)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := tx.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role, custom_role_id) VALUES ($1, $2, $3, $4)`, rm.OrgID, userID, rm.Role, rm.CustomRoleID); err != nil {
				return err
			}
			if len(validDomains) > 0 {
				if _, err := tx.Exec(ctx, `INSERT INTO organization_member_domains (org_id, user_id, domain_id) SELECT $1, $2, unnest($3::uuid[]) ON CONFLICT DO NOTHING`, rm.OrgID, userID, validDomains); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		if roleRank(existingRole) < roleRank(rm.Role) {
			if _, err := tx.Exec(ctx, `UPDATE organization_members SET role = $3, custom_role_id = $4 WHERE org_id = $1 AND user_id = $2`, rm.OrgID, userID, rm.Role, rm.CustomRoleID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM organization_member_domains WHERE org_id = $1 AND user_id = $2`, rm.OrgID, userID); err != nil {
				return err
			}
			if len(validDomains) > 0 {
				if _, err := tx.Exec(ctx, `INSERT INTO organization_member_domains (org_id, user_id, domain_id) SELECT $1, $2, unnest($3::uuid[]) ON CONFLICT DO NOTHING`, rm.OrgID, userID, validDomains); err != nil {
					return err
				}
			}
		} else if existingRole == rm.Role {
			if existingRole == roleDomainAdmin && len(validDomains) > 0 {
				if _, err := tx.Exec(ctx, `INSERT INTO organization_member_domains (org_id, user_id, domain_id) SELECT $1, $2, unnest($3::uuid[]) ON CONFLICT DO NOTHING`, rm.OrgID, userID, validDomains); err != nil {
					return err
				}
			} else if existingRole == roleCustom && existingCustomID != nil && rm.CustomRoleID != nil && *existingCustomID == *rm.CustomRoleID {
				if rm.OrgWide {
					if _, err := tx.Exec(ctx, `DELETE FROM organization_member_domains WHERE org_id = $1 AND user_id = $2`, rm.OrgID, userID); err != nil {
						return err
					}
				} else if len(validDomains) > 0 {
					var existingCount int
					if err := tx.QueryRow(ctx, `SELECT count(*) FROM organization_member_domains WHERE org_id = $1 AND user_id = $2`, rm.OrgID, userID).Scan(&existingCount); err != nil {
						return err
					}
					if existingCount > 0 {
						if _, err := tx.Exec(ctx, `INSERT INTO organization_member_domains (org_id, user_id, domain_id) SELECT $1, $2, unnest($3::uuid[]) ON CONFLICT DO NOTHING`, rm.OrgID, userID, validDomains); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

var errSharedAddress = errors.New("address belongs to a shared mailbox")

func (s *Server) linkMailboxForIdentity(ctx context.Context, tx pgx.Tx, userID, email string, provision bool) error {
	if err := lockAddress(ctx, tx, email); err != nil {
		return err
	}
	var mailboxID, orgID, kind string
	var owner *string
	err := tx.QueryRow(ctx, `SELECT id, org_id, owner_user_id, kind FROM mailboxes WHERE address = $1`, email).Scan(&mailboxID, &orgID, &owner, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		if !provision {
			return nil
		}
		local, domain, _ := strings.Cut(email, "@")
		var domainID string
		err = tx.QueryRow(ctx, `SELECT id, org_id FROM domains WHERE name = $1 AND active AND verified_at IS NOT NULL AND alias_of IS NULL`, domain).Scan(&domainID, &orgID)
		if errors.Is(err, pgx.ErrNoRows) || !localPartPattern.MatchString(local) {
			return nil
		}
		if err != nil {
			return err
		}
		quota, err := s.reserveMailbox(ctx, tx, orgID, domainID, nil)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO mailboxes (org_id, domain_id, local_part, address, owner_user_id, protocols, quota_bytes)
			SELECT $1, $2, $3, $4, $5, (SELECT mailbox_protocols FROM organizations WHERE id = $1), $6 WHERE NOT EXISTS (SELECT 1 FROM aliases WHERE address = $4)`, orgID, domainID, local, email, userID, quota)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
	} else if err != nil {
		return err
	} else if kind == "shared" {
		return errSharedAddress
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

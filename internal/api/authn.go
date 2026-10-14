package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/auth"
	"github.com/scalastudios/aether/internal/secrets"
)

const (
	localSessionTTL = 7 * 24 * time.Hour
	oidcSessionTTL  = 12 * time.Hour
)

type User struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	DisplayName     string `json:"displayName"`
	IsPlatformAdmin bool   `json:"isPlatformAdmin"`
	Status          string `json:"status"`
}

type membership struct {
	OrgID   string `json:"orgId"`
	OrgName string `json:"orgName"`
	OrgSlug string `json:"orgSlug"`
	Role    string `json:"role"`
}

type sessionResponse struct {
	User        User         `json:"user"`
	Memberships []membership `json:"memberships"`
	CSRFToken   string       `json:"csrfToken"`
}

func constantTimeEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) authProviders(w http.ResponseWriter, r *http.Request) {
	type provider struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	rows, err := s.db.Query(r.Context(), `SELECT slug, name, kind FROM oidc_providers WHERE enabled ORDER BY kind = 'halo' DESC, name`)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	providers, err := pgx.CollectRows(rows, pgx.RowToStructByPos[provider])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"localLogin": s.cfg.LocalLogin, "providers": nonNil(providers)})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.LocalLogin {
		writeError(w, http.StatusForbidden, "local_login_disabled", "Password sign-in is disabled. Use your organization's identity provider.")
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if !s.allow(r.Context(), "login-ip:"+s.clientIP(r), 20) || !s.allow(r.Context(), "login-user:"+email, 10) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many sign-in attempts. Wait a minute and try again.")
		return
	}
	var userID, hash, status string
	err := s.db.QueryRow(r.Context(), `SELECT id, coalesce(password_hash, ''), status FROM users WHERE email = $1`, email).Scan(&userID, &hash, &status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	if hash == "" {
		auth.BurnPasswordCheck(in.Password)
		s.audit(r.Context(), r, nil, nil, "auth.login.failed", "user", email, nil)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The email or password is incorrect.")
		return
	}
	ok, err := auth.VerifyPassword(hash, in.Password)
	if err != nil || !ok {
		s.audit(r.Context(), r, nil, nil, "auth.login.failed", "user", userID, nil)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The email or password is incorrect.")
		return
	}
	if status != "active" {
		writeError(w, http.StatusForbidden, "account_disabled", "This account is suspended. Contact your administrator.")
		return
	}
	csrf, err := s.startSession(r.Context(), w, r, userID, nil, nil, localSessionTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, &principal{User: User{ID: userID}}, nil, "auth.login", "user", userID, map[string]any{"method": "password"})
	resp, err := s.sessionFor(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp.CSRFToken = csrf
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID string, providerID, idToken *string, ttl time.Duration) (string, error) {
	token := secrets.Token(32)
	csrf := secrets.Token(32)
	expires := time.Now().Add(ttl)
	_, err := s.db.Exec(ctx, `INSERT INTO sessions (id_hash, user_id, csrf_token, provider_id, id_token, ip, user_agent, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		secrets.SHA256(token), userID, csrf, providerID, idToken, s.clientIP(r), truncate(r.UserAgent(), 300), expires)
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
	return csrf, nil
}

func (s *Server) sessionFor(ctx context.Context, userID string) (*sessionResponse, error) {
	resp := &sessionResponse{Memberships: []membership{}}
	err := s.db.QueryRow(ctx, `SELECT id, email, display_name, is_platform_admin, status FROM users WHERE id = $1`, userID).
		Scan(&resp.User.ID, &resp.User.Email, &resp.User.DisplayName, &resp.User.IsPlatformAdmin, &resp.User.Status)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT o.id, o.name, o.slug, m.role FROM organization_members m JOIN organizations o ON o.id = m.org_id WHERE m.user_id = $1 ORDER BY o.name`, userID)
	if err != nil {
		return nil, err
	}
	resp.Memberships, err = pgx.CollectRows(rows, pgx.RowToStructByPos[membership])
	resp.Memberships = nonNil(resp.Memberships)
	return resp, err
}

func (s *Server) session(w http.ResponseWriter, r *http.Request, p *principal) {
	resp, err := s.sessionFor(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp.CSRFToken = p.csrf
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, p *principal) {
	if _, err := s.db.Exec(r.Context(), `DELETE FROM sessions WHERE id_hash = $1`, p.sessionHash); err != nil {
		s.internalError(w, r, err)
		return
	}
	clearCookie(w, sessionCookie, s.cfg.SecureCookies())
	s.audit(r.Context(), r, p, nil, "auth.logout", "user", p.User.ID, nil)
	resp := map[string]string{}
	if p.providerID != nil {
		if endSession := s.endSessionURL(r.Context(), *p.providerID, p.idToken); endSession != "" {
			resp["redirect"] = endSession
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func safeReturnTo(v string) string {
	u, err := url.Parse(v)
	if err != nil || v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") || u.Host != "" || u.Scheme != "" {
		return "/mail"
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/auth"
	"github.com/scalastudios/aether/internal/i18n"
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
	Title           string `json:"title"`
	Phone           string `json:"phone"`
	UndoSendSeconds int    `json:"undoSendSeconds"`
	Locale          string `json:"locale"`
}

func allowedUndoDelay(seconds int) bool {
	switch seconds {
	case 0, 5, 10, 20, 30:
		return true
	default:
		return false
	}
}

type membership struct {
	OrgID          string   `json:"orgId"`
	OrgName        string   `json:"orgName"`
	OrgSlug        string   `json:"orgSlug"`
	Role           string   `json:"role"`
	CustomRoleID   *string  `json:"customRoleId,omitempty"`
	CustomRoleName string   `json:"customRoleName,omitempty"`
	DomainIDs      []string `json:"domainIds"`
	Permissions    []string `json:"permissions"`
	Compliance     bool     `json:"compliance"`
	DefaultLocale  string   `json:"defaultLocale"`
}

type sessionResponse struct {
	User                   User         `json:"user"`
	Memberships            []membership `json:"memberships"`
	CSRFToken              string       `json:"csrfToken"`
	TwoFactorSetupRequired bool         `json:"twoFactorSetupRequired"`
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
	defaultLocale := ""
	if orgID, err := s.brandingOrg(r); err == nil && orgID != "" {
		defaultLocale = s.orgDefaultLocale(r.Context(), orgID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"localLogin":    s.cfg.LocalLogin,
		"providers":     nonNil(providers),
		"defaultLocale": defaultLocale,
	})
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
	if !s.allow(r.Context(), "login-ip:"+s.clientIP(r), 100) || !s.allow(r.Context(), "login-user:"+email, 10) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many sign-in attempts. Wait a minute and try again.")
		return
	}
	if s.authBlocked(r.Context(), s.clientIP(r)) {
		writeError(w, http.StatusForbidden, "address_blocked", "Too many failed sign-ins from your network. Try again later.")
		return
	}
	var userID, hash, status string
	err := s.db.QueryRow(r.Context(), `SELECT id, coalesce(password_hash, ''), status FROM users WHERE email = $1`, email).Scan(&userID, &hash, &status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	if hash == "" {
		authAttempts.WithLabelValues("password", "failure").Inc()
		auth.BurnPasswordCheck(in.Password)
		s.recordAuthFailure(r.Context(), s.clientIP(r), email, s.passwordTag(in.Password), "web")
		s.audit(r.Context(), r, nil, nil, "auth.login.failed", "user", email, nil)
		if userID != "" && status == "active" {
			s.recordFailedSignIn(r.Context(), r, userID)
		}
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The email or password is incorrect.")
		return
	}
	ok, err := auth.VerifyPassword(hash, in.Password)
	if err != nil || !ok {
		authAttempts.WithLabelValues("password", "failure").Inc()
		s.recordAuthFailure(r.Context(), s.clientIP(r), email, s.passwordTag(in.Password), "web")
		s.audit(r.Context(), r, nil, nil, "auth.login.failed", "user", userID, nil)
		if status == "active" {
			s.recordFailedSignIn(r.Context(), r, userID)
		}
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The email or password is incorrect.")
		return
	}
	if status != "active" {
		writeError(w, http.StatusForbidden, "account_disabled", "This account is suspended. Contact your administrator.")
		return
	}
	prompt, err := s.twoFactorPrompt(r.Context(), w, userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if prompt != nil {
		writeJSON(w, http.StatusOK, map[string]any{"twoFactor": prompt})
		return
	}
	s.finishPasswordLogin(w, r, userID, "password")
}

func (s *Server) finishPasswordLogin(w http.ResponseWriter, r *http.Request, userID, method string) {
	csrf, sessionHash, err := s.startSession(r.Context(), w, r, userID, nil, nil, localSessionTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	authAttempts.WithLabelValues("password", "success").Inc()
	s.audit(r.Context(), r, &principal{User: User{ID: userID}}, nil, "auth.login", "user", userID, map[string]any{"method": method})
	s.checkSignInNotification(r.Context(), r, userID, method, "", sessionHash)
	resp, err := s.sessionFor(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp.CSRFToken = csrf
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID string, providerID, idToken *string, ttl time.Duration) (string, []byte, error) {
	token := secrets.Token(32)
	csrf := secrets.Token(32)
	expires := time.Now().Add(ttl)
	sessionHash := secrets.SHA256(token)
	_, err := s.db.Exec(ctx, `INSERT INTO sessions (id_hash, user_id, csrf_token, provider_id, id_token, ip, user_agent, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sessionHash, userID, csrf, providerID, idToken, s.clientIP(r), truncate(r.UserAgent(), 300), expires)
	if err != nil {
		return "", nil, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE mailboxes SET last_web_login_at = now() WHERE owner_user_id = $1 AND active`, userID); err != nil {
		s.log.Warn("recording web sign-in timestamp failed", "user", userID, "err", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
	return csrf, sessionHash, nil
}

func (s *Server) sessionFor(ctx context.Context, userID string) (*sessionResponse, error) {
	resp := &sessionResponse{Memberships: []membership{}}
	err := s.db.QueryRow(ctx, `SELECT id, email, display_name, is_platform_admin, status, title, phone, undo_send_seconds, locale, `+twoFactorSetupPending+` FROM users u WHERE id = $1`, userID).
		Scan(&resp.User.ID, &resp.User.Email, &resp.User.DisplayName, &resp.User.IsPlatformAdmin, &resp.User.Status, &resp.User.Title, &resp.User.Phone, &resp.User.UndoSendSeconds, &resp.User.Locale, &resp.TwoFactorSetupRequired)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT o.id, o.name, o.slug, m.role, m.custom_role_id, coalesce(r.name, ''), coalesce(r.scopes, '{}'), m.compliance,
			coalesce((SELECT array_agg(d.domain_id::text ORDER BY d.domain_id) FROM organization_member_domains d WHERE d.org_id = m.org_id AND d.user_id = m.user_id), '{}'),
			o.default_locale
		FROM organization_members m
		JOIN organizations o ON o.id = m.org_id
		LEFT JOIN organization_roles r ON r.id = m.custom_role_id
		WHERE m.user_id = $1 ORDER BY o.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item membership
		var scopes []string
		if err := rows.Scan(&item.OrgID, &item.OrgName, &item.OrgSlug, &item.Role, &item.CustomRoleID, &item.CustomRoleName, &scopes, &item.Compliance, &item.DomainIDs, &item.DefaultLocale); err != nil {
			return nil, err
		}
		item.DomainIDs = nonNil(item.DomainIDs)
		grant := buildOrgGrant(item.Role, item.CustomRoleID, item.CustomRoleName, scopes, item.DomainIDs)
		if resp.User.IsPlatformAdmin {
			grant = buildOrgGrant(roleOwner, nil, "", nil, nil)
		}
		item.Permissions = grant.permissionList()
		resp.Memberships = append(resp.Memberships, item)
	}
	return resp, rows.Err()
}

func (s *Server) session(w http.ResponseWriter, r *http.Request, p *principal) {
	resp, err := s.sessionFor(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp.CSRFToken = p.csrf
	resp.TwoFactorSetupRequired = p.twoFactorSetup
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Title           *string `json:"title"`
		Phone           *string `json:"phone"`
		Locale          *string `json:"locale"`
		UndoSendSeconds *int    `json:"undoSendSeconds"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	var title, phone, locale string
	if err := s.db.QueryRow(r.Context(), `SELECT title, phone, locale FROM users WHERE id = $1`, p.User.ID).Scan(&title, &phone, &locale); err != nil {
		s.internalError(w, r, err)
		return
	}
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
	}
	if in.Phone != nil {
		phone = strings.TrimSpace(*in.Phone)
	}
	if in.Locale != nil {
		norm, ok := i18n.Normalize(*in.Locale)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "invalid_locale", "Choose one of the supported languages, or leave empty for the default.")
			return
		}
		locale = norm
	}
	if utf8.RuneCountInString(title) > 100 || utf8.RuneCountInString(phone) > 40 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_profile", "Keep the job title to 100 characters and the phone number to 40.")
		return
	}
	if in.UndoSendSeconds != nil && !allowedUndoDelay(*in.UndoSendSeconds) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_undo_send", "Choose off, or a delay of 5, 10, 20 or 30 seconds.")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE users SET title = $2, phone = $3, locale = $4, undo_send_seconds = coalesce($5, undo_send_seconds), updated_at = now() WHERE id = $1`, p.User.ID, title, phone, locale, in.UndoSendSeconds); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.session(w, r, p)
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

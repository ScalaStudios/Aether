package api

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"rsc.io/qr"

	"github.com/scalastudios/aether/internal/auth"
	"github.com/scalastudios/aether/internal/secrets"
)

const (
	twoFactorCookie   = "aether_two_factor"
	twoFactorTTL      = 5 * time.Minute
	twoFactorAttempts = 5
	noPasswordMessage = "Your account signs in through an identity provider, which manages its password and two-factor authentication."
)

const hasTwoFactor = `(EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.enabled_at IS NOT NULL) OR EXISTS (SELECT 1 FROM user_security_keys k WHERE k.user_id = u.id))`

const twoFactorRequired = `EXISTS (SELECT 1 FROM organization_members om JOIN organizations o ON o.id = om.org_id WHERE om.user_id = u.id AND (o.two_factor_policy = 'everyone' OR (o.two_factor_policy = 'admins' AND om.role <> 'member')))`

const twoFactorSetupPending = `(` + twoFactorRequired + ` AND NOT ` + hasTwoFactor + `)`

var securityKeyTransports = []string{"usb", "nfc", "ble", "hybrid", "internal", "smart-card"}

type credentialDescriptor struct {
	Type       string   `json:"type"`
	ID         []byte   `json:"id"`
	Transports []string `json:"transports"`
}

type twoFactorPrompt struct {
	TOTP        bool           `json:"totp"`
	SecurityKey map[string]any `json:"securityKey"`
}

type securityKeyAssertion struct {
	ID                []byte `json:"id"`
	ClientDataJSON    []byte `json:"clientDataJSON"`
	AuthenticatorData []byte `json:"authenticatorData"`
	Signature         []byte `json:"signature"`
}

func (s *Server) relyingParty() auth.RelyingParty {
	return auth.RelyingParty{ID: s.cfg.PublicURL.Hostname(), Origin: s.cfg.PublicURL.Scheme + "://" + s.cfg.PublicURL.Host}
}

func twoFactorLoginKey(token string) string {
	return "aether:two-factor-login:" + secrets.SHA256Hex(token)
}

func securityKeyChallengeKey(p *principal) string {
	return "aether:security-key-challenge:" + hex.EncodeToString(p.sessionHash)
}

func newChallenge() []byte {
	challenge := make([]byte, 32)
	rand.Read(challenge)
	return challenge
}

func recoveryHash(code string) []byte {
	return secrets.SHA256(strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, code))
}

func (s *Server) credentialDescriptors(ctx context.Context, userID string) ([]credentialDescriptor, error) {
	rows, err := s.db.Query(ctx, `SELECT 'public-key', credential_id, transports FROM user_security_keys WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[credentialDescriptor])
	return nonNil(out), err
}

func (s *Server) twoFactorPrompt(ctx context.Context, w http.ResponseWriter, userID string) (*twoFactorPrompt, error) {
	var prompt twoFactorPrompt
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_totp WHERE user_id = $1 AND enabled_at IS NOT NULL)`, userID).Scan(&prompt.TOTP); err != nil {
		return nil, err
	}
	keys, err := s.credentialDescriptors(ctx, userID)
	if err != nil || (!prompt.TOTP && len(keys) == 0) {
		return nil, err
	}
	var challenge []byte
	if len(keys) > 0 {
		challenge = newChallenge()
		prompt.SecurityKey = map[string]any{"challenge": challenge, "rpId": s.relyingParty().ID, "allowCredentials": keys, "timeout": 120000, "userVerification": "discouraged"}
	}
	token := secrets.Token(32)
	key := twoFactorLoginKey(token)
	pipe := s.kv.TxPipeline()
	pipe.HSet(ctx, key, "user", userID, "challenge", challenge)
	pipe.Expire(ctx, key, twoFactorTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	http.SetCookie(w, &http.Cookie{
		Name: twoFactorCookie, Value: token, Path: "/", MaxAge: int(twoFactorTTL.Seconds()),
		HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteStrictMode,
	})
	return &prompt, nil
}

func (s *Server) loginTwoFactor(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.LocalLogin {
		writeError(w, http.StatusForbidden, "local_login_disabled", "Password sign-in is disabled. Use your organization's identity provider.")
		return
	}
	var in struct {
		Code         string                `json:"code"`
		RecoveryCode string                `json:"recoveryCode"`
		SecurityKey  *securityKeyAssertion `json:"securityKey"`
	}
	if !decode(w, r, 32<<10, &in) {
		return
	}
	ctx := r.Context()
	if !s.allow(ctx, "login-ip:"+s.clientIP(r), 100) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many sign-in attempts. Wait a minute and try again.")
		return
	}
	expired := func(message string) {
		clearCookie(w, twoFactorCookie, s.cfg.SecureCookies())
		writeError(w, http.StatusUnauthorized, "two_factor_expired", message)
	}
	c, err := r.Cookie(twoFactorCookie)
	if err != nil || c.Value == "" {
		expired("The sign-in expired. Enter your email and password again.")
		return
	}
	key := twoFactorLoginKey(c.Value)
	pipe := s.kv.TxPipeline()
	attempts := pipe.HIncrBy(ctx, key, "attempts", 1)
	pending := pipe.HGetAll(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		s.internalError(w, r, err)
		return
	}
	userID := pending.Val()["user"]
	if userID == "" || attempts.Val() > twoFactorAttempts {
		s.kv.Del(ctx, key)
		if userID == "" {
			expired("The sign-in expired. Enter your email and password again.")
		} else {
			expired("Too many wrong codes. Enter your email and password again.")
		}
		return
	}
	if !s.allow(ctx, "two-factor-user:"+userID, twoFactorAttempts) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many sign-in attempts. Wait a minute and try again.")
		return
	}
	method, ok, err := s.verifySecondFactor(ctx, userID, []byte(pending.Val()["challenge"]), in.Code, in.RecoveryCode, in.SecurityKey)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		authAttempts.WithLabelValues("two_factor", "failure").Inc()
		s.audit(ctx, r, nil, nil, "auth.login.failed", "user", userID, map[string]any{"reason": "two_factor"})
		writeError(w, http.StatusUnauthorized, "invalid_code", "That code or security key was not accepted. Try again, or use another method.")
		return
	}
	deleted, err := s.kv.Del(ctx, key).Result()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if deleted == 0 {
		expired("The sign-in expired. Enter your email and password again.")
		return
	}
	clearCookie(w, twoFactorCookie, s.cfg.SecureCookies())
	var status string
	if err := s.db.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status); err != nil {
		s.internalError(w, r, err)
		return
	}
	if status != "active" {
		writeError(w, http.StatusForbidden, "account_disabled", "This account is suspended. Contact your administrator.")
		return
	}
	s.finishPasswordLogin(w, r, userID, "password+"+method)
}

func (s *Server) verifySecondFactor(ctx context.Context, userID string, challenge []byte, code, recoveryCode string, key *securityKeyAssertion) (string, bool, error) {
	switch {
	case code != "":
		ok, err := s.checkTOTP(ctx, userID, code, true)
		return "totp", ok, err
	case recoveryCode != "":
		tag, err := s.db.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id = $1 AND code_hash = $2`, userID, recoveryHash(recoveryCode))
		return "recovery_code", err == nil && tag.RowsAffected() == 1, err
	case key != nil:
		ok, err := s.checkSecurityKey(ctx, userID, challenge, key)
		return "security_key", ok, err
	}
	return "", false, nil
}

func (s *Server) checkTOTP(ctx context.Context, userID, code string, enabled bool) (bool, error) {
	var sealed []byte
	var lastStep int64
	err := s.db.QueryRow(ctx, `SELECT secret, last_step FROM user_totp WHERE user_id = $1 AND (enabled_at IS NOT NULL) = $2`, userID, enabled).Scan(&sealed, &lastStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	secret, err := s.box.Open(sealed, "totp:"+userID)
	if err != nil {
		return false, err
	}
	step, ok := auth.VerifyTOTP(secret, strings.ReplaceAll(code, " ", ""), time.Now(), lastStep)
	if !ok {
		return false, nil
	}
	tag, err := s.db.Exec(ctx, `UPDATE user_totp SET last_step = $2 WHERE user_id = $1 AND last_step < $2`, userID, step)
	return err == nil && tag.RowsAffected() == 1, err
}

func (s *Server) checkSecurityKey(ctx context.Context, userID string, challenge []byte, a *securityKeyAssertion) (bool, error) {
	var id string
	var publicKey []byte
	var count int64
	err := s.db.QueryRow(ctx, `SELECT id, public_key, sign_count FROM user_security_keys WHERE user_id = $1 AND credential_id = $2`, userID, a.ID).Scan(&id, &publicKey, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	next, err := s.relyingParty().VerifyAssertion(a.ClientDataJSON, a.AuthenticatorData, a.Signature, publicKey, challenge, uint32(count))
	if err != nil {
		return false, nil
	}
	tag, err := s.db.Exec(ctx, `UPDATE user_security_keys SET sign_count = $3, last_used_at = now() WHERE id = $1 AND sign_count = $2`, id, count, next)
	return err == nil && tag.RowsAffected() == 1, err
}

func (s *Server) securityStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	type securityKey struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		CreatedAt  time.Time  `json:"createdAt"`
		LastUsedAt *time.Time `json:"lastUsedAt"`
	}
	var out struct {
		PasswordSignIn    bool          `json:"passwordSignIn"`
		TwoFactorRequired bool          `json:"twoFactorRequired"`
		TOTP              bool          `json:"totp"`
		RecoveryCodes     int           `json:"recoveryCodes"`
		SecurityKeys      []securityKey `json:"securityKeys"`
	}
	err := s.db.QueryRow(r.Context(), `
		SELECT u.password_hash IS NOT NULL, `+twoFactorRequired+`,
			EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = u.id AND t.enabled_at IS NOT NULL),
			(SELECT count(*) FROM user_recovery_codes c WHERE c.user_id = u.id)
		FROM users u WHERE u.id = $1`, p.User.ID).Scan(&out.PasswordSignIn, &out.TwoFactorRequired, &out.TOTP, &out.RecoveryCodes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, name, created_at, last_used_at FROM user_security_keys WHERE user_id = $1 ORDER BY created_at`, p.User.ID)
	if err == nil {
		out.SecurityKeys, err = pgx.CollectRows(rows, pgx.RowToStructByPos[securityKey])
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out.SecurityKeys = nonNil(out.SecurityKeys)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) passwordAccount(w http.ResponseWriter, r *http.Request, p *principal) bool {
	var has bool
	if err := s.db.QueryRow(r.Context(), `SELECT password_hash IS NOT NULL FROM users WHERE id = $1`, p.User.ID).Scan(&has); err != nil {
		s.internalError(w, r, err)
		return false
	}
	if !has {
		writeError(w, http.StatusConflict, "no_password", noPasswordMessage)
	}
	return has
}

func (s *Server) confirmPassword(w http.ResponseWriter, r *http.Request, p *principal, password string) bool {
	if !s.allow(r.Context(), "password-confirm:"+p.User.ID, 10) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Wait a minute and try again.")
		return false
	}
	var hash string
	if err := s.db.QueryRow(r.Context(), `SELECT coalesce(password_hash, '') FROM users WHERE id = $1`, p.User.ID).Scan(&hash); err != nil {
		s.internalError(w, r, err)
		return false
	}
	if hash == "" {
		writeError(w, http.StatusConflict, "no_password", noPasswordMessage)
		return false
	}
	if ok, err := auth.VerifyPassword(hash, password); err != nil || !ok {
		s.audit(r.Context(), r, p, nil, "auth.password.confirm_failed", "user", p.User.ID, nil)
		writeError(w, http.StatusUnprocessableEntity, "wrong_password", "The current password is incorrect.")
		return false
	}
	return true
}

func weakPassword(w http.ResponseWriter, password string) bool {
	if len(password) < 12 {
		writeError(w, http.StatusUnprocessableEntity, "weak_password", "Use a password of at least 12 characters.")
		return true
	}
	return false
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decode(w, r, 8<<10, &in) || weakPassword(w, in.NewPassword) || !s.confirmPassword(w, r, p, in.CurrentPassword) {
		return
	}
	var ended int64
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, p.User.ID, auth.HashPassword(in.NewPassword)); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1 AND id_hash <> $2`, p.User.ID, p.sessionHash)
		ended = tag.RowsAffected()
		return err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, nil, "auth.password.change", "user", p.User.ID, map[string]any{"sessionsEnded": ended})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) userHasTwoFactor(ctx context.Context, userID string) (bool, error) {
	var has bool
	err := s.db.QueryRow(ctx, `SELECT `+hasTwoFactor+` FROM users u WHERE u.id = $1`, userID).Scan(&has)
	return has, err
}

func (s *Server) replaceRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes := make([]string, 10)
	hashes := make([][]byte, len(codes))
	for i := range codes {
		t := secrets.Token(10)
		codes[i] = t[0:4] + "-" + t[4:8] + "-" + t[8:12] + "-" + t[12:16]
		hashes[i] = recoveryHash(codes[i])
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id = $1`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_recovery_codes (user_id, code_hash) SELECT $1, unnest($2::bytea[])`, userID, hashes)
		return err
	})
	return codes, err
}

func (s *Server) twoFactorAdded(w http.ResponseWriter, r *http.Request, p *principal, hadTwoFactor bool, method string) {
	s.audit(r.Context(), r, p, nil, "auth.two_factor.add", "user", p.User.ID, map[string]any{"method": method})
	codes := []string{}
	if !hadTwoFactor {
		var err error
		if codes, err = s.replaceRecoveryCodes(r.Context(), p.User.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string][]string{"recoveryCodes": codes})
}

func (s *Server) twoFactorRemoved(w http.ResponseWriter, r *http.Request, p *principal, method string) {
	if _, err := s.db.Exec(r.Context(), `DELETE FROM user_recovery_codes WHERE user_id = $1 AND NOT (SELECT `+hasTwoFactor+` FROM users u WHERE u.id = $1)`, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, nil, "auth.two_factor.remove", "user", p.User.ID, map[string]any{"method": method})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) startTOTP(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.passwordAccount(w, r, p) {
		return
	}
	secret := auth.NewTOTPSecret()
	tag, err := s.db.Exec(r.Context(), `
		INSERT INTO user_totp (user_id, secret) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET secret = EXCLUDED.secret, last_step = 0, created_at = now() WHERE user_totp.enabled_at IS NULL`,
		p.User.ID, s.box.Seal(secret, "totp:"+p.User.ID))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "totp_enabled", "An authenticator app is already set up. Turn it off first to replace it.")
		return
	}
	text := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	uri := auth.TOTPURI("Aether", p.User.Email, text)
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": text, "uri": uri, "qrCode": "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())})
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if !s.allow(r.Context(), "two-factor-setup:"+p.User.ID, 10) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Wait a minute and try again.")
		return
	}
	had, err := s.userHasTwoFactor(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ok, err := s.checkTOTP(r.Context(), p.User.ID, in.Code, false)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_code", "That code is not right. Check that the time on your phone is correct, then enter the current code.")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE user_totp SET enabled_at = now() WHERE user_id = $1 AND enabled_at IS NULL`, p.User.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.twoFactorAdded(w, r, p, had, "totp")
}

func (s *Server) deleteTOTP(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, 8<<10, &in) || !s.confirmPassword(w, r, p, in.Password) {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM user_totp WHERE user_id = $1 AND enabled_at IS NOT NULL`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.twoFactorRemoved(w, r, p, "totp")
}

func (s *Server) securityKeyChallenge(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.passwordAccount(w, r, p) {
		return
	}
	challenge := newChallenge()
	if err := s.kv.Set(r.Context(), securityKeyChallengeKey(p), challenge, twoFactorTTL).Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	existing, err := s.credentialDescriptors(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rp := s.relyingParty()
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge":              challenge,
		"rp":                     map[string]string{"id": rp.ID, "name": "Aether"},
		"user":                   map[string]any{"id": []byte(p.User.ID), "name": p.User.Email, "displayName": cmp.Or(p.User.DisplayName, p.User.Email)},
		"pubKeyCredParams":       []map[string]any{{"type": "public-key", "alg": -7}, {"type": "public-key", "alg": -8}, {"type": "public-key", "alg": -257}},
		"timeout":                120000,
		"attestation":            "none",
		"authenticatorSelection": map[string]string{"residentKey": "discouraged", "userVerification": "discouraged"},
		"excludeCredentials":     existing,
	})
}

func (s *Server) addSecurityKey(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Name       string `json:"name"`
		Credential struct {
			ID                []byte   `json:"id"`
			ClientDataJSON    []byte   `json:"clientDataJSON"`
			AuthenticatorData []byte   `json:"authenticatorData"`
			PublicKey         []byte   `json:"publicKey"`
			Transports        []string `json:"transports"`
		} `json:"credential"`
	}
	if !decode(w, r, 64<<10, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Name the security key in 100 characters or fewer.")
		return
	}
	challenge, err := s.kv.GetDel(r.Context(), securityKeyChallengeKey(p)).Bytes()
	if errors.Is(err, redis.Nil) {
		writeError(w, http.StatusUnprocessableEntity, "challenge_expired", "The request to add a security key expired. Start again.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	c := in.Credential
	count, err := s.relyingParty().VerifyRegistration(c.ClientDataJSON, c.AuthenticatorData, c.PublicKey, c.ID, challenge)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_security_key", "Aether could not verify the security key. Try again, or use another key.")
		return
	}
	had, err := s.userHasTwoFactor(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	transports := nonNil(slices.DeleteFunc(c.Transports, func(t string) bool { return !slices.Contains(securityKeyTransports, t) }))
	_, err = s.db.Exec(r.Context(), `INSERT INTO user_security_keys (user_id, name, credential_id, public_key, sign_count, transports) VALUES ($1, $2, $3, $4, $5, $6)`,
		p.User.ID, in.Name, c.ID, c.PublicKey, int64(count), transports)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "security_key_exists", "This security key is already registered.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.twoFactorAdded(w, r, p, had, "security_key")
}

func (s *Server) deleteSecurityKey(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, 8<<10, &in) || !s.confirmPassword(w, r, p, in.Password) {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM user_security_keys WHERE id = $1 AND user_id = $2`, id, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.twoFactorRemoved(w, r, p, "security_key")
}

func (s *Server) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, 8<<10, &in) || !s.confirmPassword(w, r, p, in.Password) {
		return
	}
	has, err := s.userHasTwoFactor(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !has {
		writeError(w, http.StatusConflict, "two_factor_off", "Set up an authenticator app or a security key first. Recovery codes only work with two-factor authentication.")
		return
	}
	codes, err := s.replaceRecoveryCodes(r.Context(), p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, nil, "auth.recovery_codes.regenerate", "user", p.User.ID, nil)
	writeJSON(w, http.StatusOK, map[string][]string{"recoveryCodes": codes})
}

func (s *Server) getOrgSecurity(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var policy string
	if err := s.db.QueryRow(r.Context(), `SELECT two_factor_policy FROM organizations WHERE id = $1`, orgID).Scan(&policy); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"twoFactorPolicy": policy})
}

func (s *Server) saveOrgSecurity(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		TwoFactorPolicy string `json:"twoFactorPolicy"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if !slices.Contains([]string{"off", "admins", "everyone"}, in.TwoFactorPolicy) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_policy", "The two-factor requirement must be off, admins, or everyone.")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE organizations SET two_factor_policy = $2 WHERE id = $1`, orgID, in.TwoFactorPolicy); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "organization.security_update", "organization", orgID, map[string]any{"twoFactorPolicy": in.TwoFactorPolicy})
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, 8<<10, &in) || weakPassword(w, in.Password) {
		return
	}
	if id == p.User.ID {
		writeError(w, http.StatusUnprocessableEntity, "self_reset", "Change your own password under Settings, Security.")
		return
	}
	var hasPassword bool
	err := s.db.QueryRow(r.Context(), `SELECT password_hash IS NOT NULL FROM users WHERE id = $1`, id).Scan(&hasPassword)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !hasPassword {
		writeError(w, http.StatusConflict, "no_password", "This account signs in through an identity provider and has no Aether password.")
		return
	}
	var ended int64
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, auth.HashPassword(in.Password)); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1`, id)
		ended = tag.RowsAffected()
		return err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, nil, "user.password_reset", "user", id, map[string]any{"sessionsEnded": ended})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetUserTwoFactor(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if id == p.User.ID {
		writeError(w, http.StatusUnprocessableEntity, "self_reset", "Manage your own two-factor authentication under Settings, Security.")
		return
	}
	var exists bool
	err := s.db.QueryRow(r.Context(), `
		WITH totp AS (DELETE FROM user_totp WHERE user_id = $1), keys AS (DELETE FROM user_security_keys WHERE user_id = $1), codes AS (DELETE FROM user_recovery_codes WHERE user_id = $1)
		SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !exists {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, nil, "user.two_factor_reset", "user", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

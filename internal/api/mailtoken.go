package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const mailTokenCacheTTL = 5 * time.Minute

type introspection struct {
	Active   bool   `json:"active"`
	Subject  string `json:"sub"`
	ClientID string `json:"client_id"`
	Audience any    `json:"aud"`
	Expiry   int64  `json:"exp"`
}

func (i *introspection) audiences() []string {
	switch v := i.Audience.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, a := range v {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func internalCaller(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && !publicAddress(ap.Addr())
}

func (s *Server) verifyMailToken(w http.ResponseWriter, r *http.Request) {
	if !internalCaller(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	address := strings.ToLower(strings.TrimSpace(r.PostFormValue("user")))
	token := r.PostFormValue("token")
	if address == "" || token == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	sum := sha256.Sum256([]byte(token))
	cacheKey := "aether:mailtoken:" + hex.EncodeToString(sum[:])
	userID, err := s.kv.Get(r.Context(), cacheKey).Result()
	if err != nil {
		var expiry time.Time
		var ok bool
		userID, expiry, ok = s.introspectMailToken(r.Context(), token)
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		ttl := mailTokenCacheTTL
		if !expiry.IsZero() {
			ttl = min(ttl, time.Until(expiry))
		}
		if ttl > 0 {
			s.kv.Set(r.Context(), cacheKey, userID, ttl)
		}
	}
	var allowed bool
	err = s.db.QueryRow(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM mailboxes m JOIN domains d ON d.id = m.domain_id JOIN users u ON u.id = $2
		WHERE m.address = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL AND u.status = 'active' AND $3 = ANY(m.protocols) AND `+signInAsMailbox("$2")+`)`,
		address, userID, r.PostFormValue("service")).Scan(&allowed)
	if err != nil {
		s.log.Warn("mail token check failed", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if session := r.PostFormValue("session"); session != "" {
		_, err := s.db.Exec(r.Context(), `
			INSERT INTO mail_sessions (session, user_id, mailbox_id, client)
			SELECT $1, $2, m.id, 'token' FROM mailboxes m LEFT JOIN mailbox_audit_settings st ON st.org_id = m.org_id
			WHERE m.address = $3 AND (m.owner_user_id IS DISTINCT FROM $2 OR coalesce(st.enabled AND st.log_owner_deletes, false))
			ON CONFLICT DO NOTHING`, session, userID, address)
		if err != nil {
			s.log.Warn("recording the mail session failed", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	if r.PostFormValue("service") == "smtp" {
		var login string
		err := s.db.QueryRow(r.Context(), `
			SELECT ms.login FROM mail_senders ms JOIN mailboxes own ON own.address = ms.login AND own.owner_user_id = $2
			JOIN mailboxes m ON m.address = ms.sender AND m.owner_user_id IS DISTINCT FROM $2
			WHERE ms.sender = $1 ORDER BY ms.login LIMIT 1`, address, userID).Scan(&login)
		if err == nil {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(login))
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("mail token check failed", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	s.recordMailLogin(r.Context(), address, r.PostFormValue("service"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) introspectMailToken(ctx context.Context, token string) (string, time.Time, bool) {
	rows, err := s.db.Query(ctx, `SELECT `+providerColumns+` FROM oidc_providers WHERE enabled`)
	if err != nil {
		return "", time.Time{}, false
	}
	var providers []*providerRow
	for rows.Next() {
		if p, err := scanProvider(rows); err == nil {
			providers = append(providers, p)
		}
	}
	rows.Close()
	audience := strings.TrimRight(s.cfg.PublicURL.String(), "/")
	for _, p := range providers {
		c, err := s.oidcClientFor(ctx, p)
		if err != nil || c.meta.Introspection == "" {
			continue
		}
		info, err := introspect(ctx, c, token)
		if err != nil || !info.Active || info.Subject == "" {
			continue
		}
		aud := info.audiences()
		if info.ClientID != p.ClientID && !slices.Contains(aud, p.ClientID) && !slices.Contains(aud, audience) {
			continue
		}
		var userID string
		if err := s.db.QueryRow(ctx, `SELECT user_id FROM user_identities WHERE provider_id = $1 AND subject = $2`, p.ID, info.Subject).Scan(&userID); err != nil {
			continue
		}
		var expiry time.Time
		if info.Expiry > 0 {
			expiry = time.Unix(info.Expiry, 0)
		}
		return userID, expiry, true
	}
	return "", time.Time{}, false
}

func introspect(ctx context.Context, c *oidcClient, token string) (*introspection, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.meta.Introspection, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(c.oauth.ClientID), url.QueryEscape(c.oauth.ClientSecret))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var info introspection
	if resp.StatusCode != http.StatusOK {
		return &info, nil
	}
	return &info, json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&info)
}

package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

const authFailureWindow = 10 * time.Minute

type blockedAddress struct {
	ID           string    `json:"id"`
	Address      string    `json:"address"`
	Protocol     string    `json:"protocol"`
	Failures     int       `json:"failures"`
	Blocks       int       `json:"blocks"`
	BlockedAt    time.Time `json:"blockedAt"`
	BlockedUntil time.Time `json:"blockedUntil"`
}

func blockKey(ip string) (string, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", false
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String(), true
	}
	return netip.PrefixFrom(addr, 64).Masked().String(), true
}

func (s *Server) authBlockExempt(ip string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return true
	}
	addr = addr.Unmap().WithZone("")
	for _, n := range s.cfg.AuthBlockAllowNets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Server) authBlocked(ctx context.Context, ip string) bool {
	key, ok := blockKey(ip)
	if !ok {
		return false
	}
	var blocked bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM blocked_addresses WHERE address = $1 AND blocked_until > now())`, key).Scan(&blocked); err != nil {
		s.log.Warn("blocked address check failed", "err", err)
		return false
	}
	return blocked
}

func (s *Server) passwordTag(password string) string {
	mac := hmac.New(sha256.New, s.cfg.SecretKey)
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

func (s *Server) recordAuthFailure(ctx context.Context, ip, login, secretTag, protocol string) {
	key, ok := blockKey(ip)
	if s.cfg.AuthBlockThreshold <= 0 || !ok || s.authBlockExempt(ip) {
		return
	}
	kvKey := "aether:authfail:" + key
	now := time.Now()
	pipe := s.kv.TxPipeline()
	pipe.ZAdd(ctx, kvKey, redis.Z{Score: float64(now.UnixMilli()), Member: strings.ToLower(login) + "\n" + secretTag})
	pipe.ZRemRangeByScore(ctx, kvKey, "-inf", strconv.FormatInt(now.Add(-authFailureWindow).UnixMilli(), 10))
	count := pipe.ZCard(ctx, kvKey)
	pipe.Expire(ctx, kvKey, authFailureWindow)
	if _, err := pipe.Exec(ctx); err != nil {
		s.log.Warn("recording failed sign-in failed", "err", err)
		return
	}
	if count.Val() < int64(s.cfg.AuthBlockThreshold) {
		return
	}
	s.kv.Del(ctx, kvKey)
	var blocks int
	var until time.Time
	err := s.db.QueryRow(ctx, `
		INSERT INTO blocked_addresses AS b (address, protocol, failures, blocked_until)
		VALUES ($1, $2, $3, now() + interval '15 minutes')
		ON CONFLICT (address) DO UPDATE SET
			protocol = excluded.protocol, failures = excluded.failures, blocked_at = now(),
			blocks = CASE WHEN b.blocked_until > now() - interval '1 day' THEN b.blocks + 1 ELSE 1 END,
			blocked_until = now() + least(interval '15 minutes' * power(2, CASE WHEN b.blocked_until > now() - interval '1 day' THEN least(b.blocks, 7) ELSE 0 END), interval '1 day')
		WHERE b.blocked_until <= now()
		RETURNING blocks, blocked_until`, key, protocol, count.Val()).Scan(&blocks, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		s.log.Error("blocking address failed", "address", key, "err", err)
		return
	}
	s.db.Exec(ctx, `DELETE FROM blocked_addresses WHERE blocked_until < now() - interval '1 day'`)
	authBlocks.WithLabelValues(protocol).Inc()
	s.log.Warn("blocked address after failed sign-ins", "address", key, "protocol", protocol, "failures", count.Val(), "until", until)
	meta := map[string]any{"protocol": protocol, "failures": count.Val(), "blocks": blocks, "blockedUntil": until}
	if _, err := s.db.Exec(ctx, `INSERT INTO audit_events (action, target_type, target_id, ip, metadata) VALUES ('auth.address.blocked', 'address', $1, $1, $2)`, key, meta); err != nil {
		s.log.Error("audit write failed", "action", "auth.address.blocked", "err", err)
	}
}

func policyProtocol(p string) string {
	switch p {
	case "imap", "pop3", "smtp", "submission", "sieve", "managesieve", "lmtp":
		return p
	}
	return "mail"
}

func (s *Server) authPolicy(w http.ResponseWriter, r *http.Request) {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err != nil || publicAddress(ap.Addr()) || !constantTimeEqual(r.Header.Get("X-API-Key"), s.cfg.DovecotAPIKey) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var in struct {
		Login        string `json:"login"`
		PasswordHash string `json:"pwhash"`
		Remote       string `json:"remote"`
		Protocol     string `json:"protocol"`
		Success      bool   `json:"success"`
		PolicyReject bool   `json:"policy_reject"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": 0})
		return
	}
	protocol := policyProtocol(in.Protocol)
	resp := map[string]any{"status": 0, "msg": ""}
	switch r.URL.Query().Get("command") {
	case "allow":
		if s.authBlocked(r.Context(), in.Remote) {
			authAttempts.WithLabelValues(protocol, "blocked").Inc()
			resp["status"], resp["msg"] = -1, "Too many failed sign-ins from this address. Try again later."
		}
	case "report":
		switch {
		case in.PolicyReject:
		case in.Success:
			authAttempts.WithLabelValues(protocol, "success").Inc()
		default:
			authAttempts.WithLabelValues(protocol, "failure").Inc()
			s.recordAuthFailure(r.Context(), in.Remote, in.Login, in.PasswordHash, protocol)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listBlockedAddresses(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, address, protocol, failures, blocks, blocked_at, blocked_until FROM blocked_addresses WHERE blocked_until > now() ORDER BY blocked_at DESC`)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[blockedAddress])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) unblockAddress(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var address string
	err := s.db.QueryRow(r.Context(), `DELETE FROM blocked_addresses WHERE id = $1 RETURNING address`, id).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.kv.Del(r.Context(), "aether:authfail:"+address)
	s.audit(r.Context(), r, p, nil, "auth.address.unblocked", "address", address, nil)
	w.WriteHeader(http.StatusNoContent)
}

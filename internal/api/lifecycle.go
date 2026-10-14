package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) setUserStatus(ctx context.Context, userID, status, by string) (bool, error) {
	var changed bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET status = $2, suspended_by = CASE WHEN $2 = 'active' THEN '' ELSE $3 END, updated_at = now() WHERE id = $1 AND status <> $2`, userID, status, by)
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() > 0
		if status != "active" {
			_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
		}
		return err
	})
	return changed, err
}

func (s *Server) updateUserStatus(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if in.Status != "active" && in.Status != "suspended" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_status", "Status must be active or suspended.")
		return
	}
	if id == p.User.ID {
		writeError(w, http.StatusUnprocessableEntity, "self_suspend", "You cannot change the status of your own account.")
		return
	}
	var exists bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists); err != nil {
		s.internalError(w, r, err)
		return
	}
	if !exists {
		notFound(w)
		return
	}
	changed, err := s.setUserStatus(r.Context(), id, in.Status, "admin")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if changed {
		s.audit(r.Context(), r, p, nil, "user."+map[string]string{"active": "restore", "suspended": "suspend"}[in.Status], "user", id, map[string]any{"source": "console"})
	}
	w.WriteHeader(http.StatusNoContent)
}

var providerSuspendEvents = map[string]string{
	"user.suspend":          "suspended",
	"scim.user.suspend":     "suspended",
	"scim.user.deprovision": "suspended",
	"user.restore":          "active",
	"scim.user.restore":     "active",
	"session.revoke":        "",
}

func verifySignature(header string, body, secret []byte, now time.Time) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(unix, 0)).Abs() > 5*time.Minute {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func (s *Server) providerWebhook(w http.ResponseWriter, r *http.Request) {
	var providerID string
	var sealed []byte
	err := s.db.QueryRow(r.Context(), `SELECT id, webhook_secret FROM oidc_providers WHERE slug = $1 AND enabled AND webhook_secret IS NOT NULL`, r.PathValue("slug")).Scan(&providerID, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	secret, err := s.box.Open(sealed, "oidc-webhook:"+providerID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil || !verifySignature(r.Header.Get("Halo-Signature"), body, secret, time.Now()) {
		webhookEvents.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusUnauthorized, "invalid_signature", "The webhook signature is missing, expired, or wrong.")
		return
	}
	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			TargetType string `json:"targetType"`
			TargetID   string `json:"targetId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil || event.ID == "" {
		writeError(w, http.StatusBadRequest, "invalid_event", "The webhook body is not a valid event.")
		return
	}
	status, handled := providerSuspendEvents[event.Type]
	if !handled || event.Data.TargetType != "user" || event.Data.TargetID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	tag, err := s.db.Exec(r.Context(), `INSERT INTO provider_webhook_events (provider_id, event_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, providerID, event.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var userID, current, suspendedBy string
	err = s.db.QueryRow(r.Context(), `SELECT u.id, u.status, u.suspended_by FROM user_identities i JOIN users u ON u.id = i.user_id WHERE i.provider_id = $1 AND i.subject = $2`, providerID, event.Data.TargetID).Scan(&userID, &current, &suspendedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	by := "provider:" + providerID
	switch {
	case status == "":
		_, err = s.db.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1`, userID)
	case status == "active" && suspendedBy != by:
	default:
		_, err = s.setUserStatus(r.Context(), userID, status, by)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	webhookEvents.WithLabelValues("applied").Inc()
	s.audit(r.Context(), r, nil, nil, "identity_provider.event", "user", userID, map[string]any{"event": event.Type, "eventId": event.ID})
	w.WriteHeader(http.StatusNoContent)
}

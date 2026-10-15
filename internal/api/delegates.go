package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func mailboxAccess(userParam string) string {
	return `(m.owner_user_id = ` + userParam + ` OR EXISTS (SELECT 1 FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = ` + userParam + `))`
}

func fullMailboxAccess(userParam string) string {
	return `(m.owner_user_id = ` + userParam + ` OR EXISTS (SELECT 1 FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = ` + userParam + ` AND md.access = 'full'))`
}

func mailboxAccessLevel(userParam string) string {
	return `CASE WHEN m.owner_user_id = ` + userParam + ` THEN 'owner' ELSE coalesce((SELECT md.access FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = ` + userParam + `), 'none') END`
}

type delegate struct {
	UserID      string    `json:"userId"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Access      string    `json:"access"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (s *Server) listDelegates(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT u.id, u.email, u.display_name, md.access, md.created_at FROM mailbox_delegates md JOIN users u ON u.id = md.user_id
		WHERE md.mailbox_id = $1 ORDER BY u.email`, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[delegate])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) addDelegate(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Email  string `json:"email"`
		Access string `json:"access"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	var d delegate
	if in.Access == "" {
		in.Access = "full"
	}
	if in.Access != "read" && in.Access != "full" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_access", "Access must be read or full.")
		return
	}
	d.Access = in.Access
	err := s.db.QueryRow(r.Context(), `
		SELECT u.id, u.email, u.display_name FROM users u
		WHERE u.email = $1 AND u.status = 'active'
		  AND (EXISTS (SELECT 1 FROM organization_members om WHERE om.user_id = u.id AND om.org_id = $2)
		    OR EXISTS (SELECT 1 FROM mailboxes x WHERE x.owner_user_id = u.id AND x.org_id = $2))`,
		strings.ToLower(strings.TrimSpace(in.Email)), m.OrgID).Scan(&d.UserID, &d.Email, &d.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnprocessableEntity, "unknown_user", "No active person in this organization uses that address. Add them as a member or give them a mailbox first.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if m.OwnerUserID != nil && *m.OwnerUserID == d.UserID {
		writeError(w, http.StatusUnprocessableEntity, "already_owner", "This person already owns the mailbox.")
		return
	}
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `
			INSERT INTO mailbox_delegates (mailbox_id, user_id, access) VALUES ($1, $2, $3)
			ON CONFLICT (mailbox_id, user_id) DO UPDATE SET access = EXCLUDED.access RETURNING created_at`, m.ID, d.UserID, d.Access).Scan(&d.CreatedAt); err != nil {
			return err
		}
		if d.Access == "full" {
			return nil
		}
		_, err := tx.Exec(r.Context(), `UPDATE app_passwords SET revoked_at = now() WHERE mailbox_id = $1 AND user_id = $2 AND revoked_at IS NULL`, m.ID, d.UserID)
		return err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.delegate_add", "mailbox", m.ID, map[string]any{"address": m.Address, "delegate": d.Email, "access": d.Access})
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) removeDelegate(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	userID, ok := pathID(w, r, "userId")
	if !ok {
		return
	}
	var email string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `
			DELETE FROM mailbox_delegates md USING users u WHERE md.mailbox_id = $1 AND md.user_id = $2 AND u.id = md.user_id RETURNING u.email`,
			m.ID, userID).Scan(&email); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `UPDATE app_passwords SET revoked_at = now() WHERE mailbox_id = $1 AND user_id = $2 AND revoked_at IS NULL`, m.ID, userID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.delegate_remove", "mailbox", m.ID, map[string]any{"address": m.Address, "delegate": email})
	w.WriteHeader(http.StatusNoContent)
}

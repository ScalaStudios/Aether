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

func mailboxAccessLevel(userParam string) string {
	return `CASE WHEN m.owner_user_id = ` + userParam + ` THEN 'owner' ELSE coalesce((SELECT md.access FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = ` + userParam + `), 'none') END`
}

func signInAsMailbox(userParam string) string {
	return `(m.kind = 'user' AND (m.owner_user_id = ` + userParam + ` OR EXISTS (SELECT 1 FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = ` + userParam + ` AND md.access = 'full' AND md.send_as)))`
}

func mailboxSendAs(userParam string) string {
	return `(coalesce(m.owner_user_id = ` + userParam + `, false) OR EXISTS (SELECT 1 FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = ` + userParam + ` AND md.send_as))`
}

func mailboxSendOnBehalf(userParam string) string {
	return `EXISTS (SELECT 1 FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = ` + userParam + ` AND md.send_on_behalf)`
}

type delegate struct {
	UserID       string    `json:"userId"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName"`
	Access       string    `json:"access"`
	SendAs       bool      `json:"sendAs"`
	SendOnBehalf bool      `json:"sendOnBehalf"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (s *Server) listDelegates(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT u.id, u.email, u.display_name, md.access, md.send_as, md.send_on_behalf, md.created_at FROM mailbox_delegates md JOIN users u ON u.id = md.user_id
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
		Email        string `json:"email"`
		Access       string `json:"access"`
		SendAs       *bool  `json:"sendAs"`
		SendOnBehalf *bool  `json:"sendOnBehalf"`
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
	var lowered bool
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		var prevAccess string
		var prevSendAs, prevSendOnBehalf bool
		err := tx.QueryRow(r.Context(), `SELECT access, send_as, send_on_behalf FROM mailbox_delegates WHERE mailbox_id = $1 AND user_id = $2`, m.ID, d.UserID).
			Scan(&prevAccess, &prevSendAs, &prevSendOnBehalf)
		existed := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(r.Context(), `
			INSERT INTO mailbox_delegates (mailbox_id, user_id, access, send_as, send_on_behalf) VALUES ($1, $2, $3, coalesce($4, false), coalesce($5, false))
			ON CONFLICT (mailbox_id, user_id) DO UPDATE SET access = EXCLUDED.access, send_as = coalesce($4, mailbox_delegates.send_as), send_on_behalf = coalesce($5, mailbox_delegates.send_on_behalf)
			RETURNING send_as, send_on_behalf, created_at`, m.ID, d.UserID, d.Access, in.SendAs, in.SendOnBehalf).Scan(&d.SendAs, &d.SendOnBehalf, &d.CreatedAt); err != nil {
			return err
		}
		lowered = existed && ((prevAccess == "full" && d.Access == "read") || (prevSendAs && !d.SendAs) || (prevSendOnBehalf && !d.SendOnBehalf))
		if lowered {
			return s.revokeMailAccess(r.Context(), tx, d.UserID, []string{m.ID})
		}
		_, err = tx.Exec(r.Context(), `UPDATE mailboxes SET `+markACLPending+` WHERE id = $1 AND kind = 'shared'`, m.ID)
		return err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.delegate_add", "mailbox", m.ID, map[string]any{"address": m.Address, "delegate": d.Email, "access": d.Access, "sendAs": d.SendAs, "sendOnBehalf": d.SendOnBehalf})
	if m.Kind == "shared" {
		if err := s.syncMailboxACL(r.Context(), m.ID); err != nil {
			s.log.Warn("mailbox ACL sync failed; retrying", "mailbox", m.ID, "err", err)
		}
	}
	wake(s.kickWake)
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
		return s.revokeMailAccess(r.Context(), tx, userID, []string{m.ID})
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
	if m.Kind == "shared" {
		if err := s.syncMailboxACL(r.Context(), m.ID); err != nil {
			s.log.Warn("mailbox ACL sync failed; retrying", "mailbox", m.ID, "err", err)
		}
	}
	wake(s.kickWake)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) managedMailbox(w http.ResponseWriter, r *http.Request, p *principal) (*ownedMailbox, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, false
	}
	m := &ownedMailbox{}
	err := s.db.QueryRow(r.Context(), `
		SELECT m.id, m.org_id, m.address, m.display_name, m.kind FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.id = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL`, id).
		Scan(&m.ID, &m.OrgID, &m.Address, &m.DisplayName, &m.Kind)
	if err == nil && p.canAdminOrg(m.OrgID) {
		return m, true
	}
	if p.apiKey != nil {
		notFound(w)
		return nil, false
	}
	return s.ownedMailbox(w, r, p)
}

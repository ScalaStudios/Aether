package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

func graceDays(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("graceDays")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 3650 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_grace", "graceDays must be a whole number of days from 0 to 3650.")
		return 0, false
	}
	return n, true
}

func (s *Server) reloadMailbox(ctx context.Context, m *mailbox) error {
	return s.db.QueryRow(ctx, `SELECT `+mailboxColumns+` FROM mailboxes WHERE id = $1`, m.ID).Scan(
		&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt, &m.Kind, &m.PurgeAfter, &m.OnHold)
}

func (s *Server) closeAndKeepMail(w http.ResponseWriter, r *http.Request, p *principal, m *mailbox, meta map[string]any) {
	if m.Active {
		if err := s.retireMailbox(r.Context(), m, false); err != nil {
			s.retireError(w, r, err)
			return
		}
	}
	if err := s.reloadMailbox(r.Context(), m); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.delete", "mailbox", m.ID, meta)
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) scheduleMailboxPurge(w http.ResponseWriter, r *http.Request, p *principal, m *mailbox, days int) {
	if m.Active {
		if err := s.retireMailbox(r.Context(), m, false); err != nil {
			s.retireError(w, r, err)
			return
		}
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE mailboxes SET purge_after = now() + make_interval(days => $2) WHERE id = $1`, m.ID, days); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.reloadMailbox(r.Context(), m); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.delete", "mailbox", m.ID, map[string]any{"address": m.Address, "graceDays": days, "purgeAfter": m.PurgeAfter})
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) deleteMailboxNow(w http.ResponseWriter, r *http.Request, p *principal, m *mailbox) {
	removed := false
	if m.Active || m.PurgeAfter != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		err := s.mail.DeleteStoredMail(ctx, m.Address)
		cancel()
		if err != nil {
			s.log.Warn("deleting stored mail failed", "mailbox", m.ID, "err", err)
			writeError(w, http.StatusServiceUnavailable, "mail_unavailable", "The mail server could not be reached to delete this mailbox's mail. Nothing was changed; try again.")
			return
		}
		removed = true
	}
	if err := s.retireMailbox(r.Context(), m, true); err != nil {
		if removed {
			writeError(w, http.StatusServiceUnavailable, "mail_unavailable", "The mail was removed but the mailbox could not be deleted. Try again.")
			return
		}
		s.retireError(w, r, err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `INSERT INTO mail_purges (mailbox_id) VALUES ($1) ON CONFLICT DO NOTHING`, m.ID); err != nil {
		s.log.Error("queueing mailbox directory removal failed", "mailbox", m.ID, "err", err)
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.delete", "mailbox", m.ID, map[string]any{"address": m.Address, "graceDays": 0})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) restoreMailbox(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	if m.PurgeAfter == nil {
		writeError(w, http.StatusUnprocessableEntity, "not_deleting", "This mailbox is not scheduled for deletion.")
		return
	}
	activate := m.Kind == "shared"
	if activate {
		if _, err := s.db.Exec(r.Context(), `UPDATE mailboxes SET `+markACLPending+` WHERE id = $1`, m.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	err := s.db.QueryRow(r.Context(), `
		UPDATE mailboxes SET active = true, purge_after = NULL WHERE id = $1 AND purge_after IS NOT NULL RETURNING `+mailboxColumns, m.ID).Scan(
		&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt, &m.Kind, &m.PurgeAfter, &m.OnHold)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.restore", "mailbox", m.ID, map[string]any{"address": m.Address})
	if activate {
		if err := s.syncMailboxACL(r.Context(), m.ID); err != nil {
			s.log.Warn("mailbox ACL sync failed; retrying", "mailbox", m.ID, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) purgeDueMailboxes(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT id, address FROM mailboxes WHERE purge_after IS NOT NULL AND purge_after <= now() AND NOT on_hold AND NOT EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = mailboxes.id) ORDER BY purge_after LIMIT 5`)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("listing mailboxes due for deletion failed", "err", err)
		}
		return
	}
	type due struct{ id, address string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.address); err != nil {
			rows.Close()
			return
		}
		list = append(list, d)
	}
	rows.Close()
	for _, d := range list {
		var still bool
		if err := s.db.QueryRow(ctx, `SELECT purge_after IS NOT NULL AND purge_after <= now() AND NOT on_hold AND NOT EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = mailboxes.id) FROM mailboxes WHERE id = $1`, d.id).Scan(&still); err != nil || !still {
			continue
		}
		delCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := s.mail.DeleteStoredMail(delCtx, d.address)
		cancel()
		if err != nil {
			s.log.Warn("deleting stored mail failed; retrying", "mailbox", d.id, "err", err)
			continue
		}
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `DELETE FROM mailboxes WHERE id = $1 AND purge_after IS NOT NULL AND purge_after <= now() AND NOT on_hold AND NOT EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = mailboxes.id)`, d.id)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO mail_purges (mailbox_id) VALUES ($1) ON CONFLICT DO NOTHING`, d.id)
			return err
		})
		if err != nil && ctx.Err() == nil {
			s.log.Error("removing a mailbox after its grace period failed", "mailbox", d.id, "err", err)
		}
	}
}

func (s *Server) dovecotCaller(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || publicAddress(addr) {
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	got := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(got) < len(prefix) || got[:len(prefix)] != prefix {
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	token := got[len(prefix):]
	want := s.cfg.DovecotAPIKey
	if len(token) != len(want) || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) listMailPurges(w http.ResponseWriter, r *http.Request) {
	if !s.dovecotCaller(w, r) {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT mailbox_id FROM mail_purges ORDER BY created_at LIMIT 50`)
	if err != nil {
		s.log.Error("listing mailbox directory removals failed", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return
		}
		fmt.Fprintln(w, id)
	}
}

func (s *Server) ackMailPurge(w http.ResponseWriter, r *http.Request) {
	if !s.dovecotCaller(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM mail_purges WHERE mailbox_id = $1`, id); err != nil {
		s.log.Error("acknowledging a mailbox directory removal failed", "mailbox", id, "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

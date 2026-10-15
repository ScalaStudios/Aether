package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	maxScheduledPerMailbox = 100
	maxScheduleAhead       = 366 * 24 * time.Hour
	maxSendAttempts        = 5
)

type scheduledMessage struct {
	ID      string            `json:"id"`
	SendAt  time.Time         `json:"sendAt"`
	Subject string            `json:"subject"`
	To      []mailpkg.Address `json:"to"`
	Error   *string           `json:"error"`
}

func (s *Server) scheduleMessage(w http.ResponseWriter, r *http.Request, m *ownedMailbox, in *outgoingRequest, out *mailpkg.Outgoing) {
	sendAt := in.SendAt.UTC()
	if sendAt.Before(time.Now().Add(5*time.Second)) || sendAt.After(time.Now().Add(maxScheduleAhead)) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_send_at", "Choose a send time in the future and within a year.")
		return
	}
	var pending int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM scheduled_messages WHERE mailbox_id = $1`, m.ID).Scan(&pending); err != nil {
		s.internalError(w, r, err)
		return
	}
	if pending >= maxScheduledPerMailbox {
		writeError(w, http.StatusUnprocessableEntity, "too_many_scheduled", "This mailbox already has 100 scheduled messages.")
		return
	}
	out.Date = sendAt
	raw, _, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	bcc := []string{}
	for _, a := range out.Bcc {
		bcc = append(bcc, a.Address)
	}
	to := out.To
	if to == nil {
		to = []mailpkg.Address{}
	}
	var item scheduledMessage
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO scheduled_messages (mailbox_id, send_at, recipients, bcc, to_addresses, subject, in_reply_to, raw)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, send_at, subject, to_addresses, error`,
		m.ID, sendAt, out.Recipients(), bcc, to, out.Subject, out.InReplyTo, raw).
		Scan(&item.ID, &item.SendAt, &item.Subject, &item.To, &item.Error)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if in.DraftUID != 0 {
		if err := s.mail.DeleteDraft(m.Address, in.DraftUID); err != nil {
			s.log.Error("removing scheduled draft failed", "mailbox", m.ID, "err", err)
		}
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) listScheduled(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, send_at, subject, to_addresses, error FROM scheduled_messages WHERE mailbox_id = $1 ORDER BY send_at`, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (scheduledMessage, error) {
		var it scheduledMessage
		return it, row.Scan(&it.ID, &it.SendAt, &it.Subject, &it.To, &it.Error)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) cancelScheduled(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "scheduledId")
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var raw []byte
	var bcc []string
	err = tx.QueryRow(r.Context(), `DELETE FROM scheduled_messages WHERE id = $1 AND mailbox_id = $2 RETURNING raw, bcc`, id, m.ID).Scan(&raw, &bcc)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(bcc) > 0 {
		raw = append([]byte("Bcc: "+strings.Join(bcc, ", ")+"\r\n"), raw...)
	}
	uid, folder, err := s.mail.SaveDraft(m.Address, raw, 0)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.mail.DeleteDraft(m.Address, uid)
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid, "folder": folder})
}

type snoozedMessage struct {
	UID          uint32    `json:"uid"`
	Until        time.Time `json:"until"`
	ReturnFolder string    `json:"returnFolder"`
}

func (s *Server) snoozeMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Until time.Time `json:"until"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if in.Until.Before(time.Now().Add(5*time.Second)) || in.Until.After(time.Now().Add(maxScheduleAhead)) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_until", "Choose a time in the future and within a year.")
		return
	}
	folder := folderParam(r)
	validity, snoozedUID, err := s.mail.Snooze(m.Address, folder, uid)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO snoozed_messages (mailbox_id, uid_validity, uid, return_folder, until) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (mailbox_id, uid_validity, uid) DO UPDATE SET return_folder = EXCLUDED.return_folder, until = EXCLUDED.until`,
		m.ID, validity, snoozedUID, folder, in.Until.UTC())
	if err != nil {
		s.mail.Wake(m.Address, validity, snoozedUID, folder)
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snoozedMessage{UID: snoozedUID, Until: in.Until.UTC(), ReturnFolder: folder})
}

func (s *Server) listSnoozed(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT uid, until, return_folder FROM snoozed_messages WHERE mailbox_id = $1 ORDER BY until`, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (snoozedMessage, error) {
		var it snoozedMessage
		return it, row.Scan(&it.UID, &it.Until, &it.ReturnFolder)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) unsnoozeMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	var validity uint32
	var folder string
	err := s.db.QueryRow(r.Context(), `SELECT uid_validity, return_folder FROM snoozed_messages WHERE mailbox_id = $1 AND uid = $2 ORDER BY created_at DESC LIMIT 1`, m.ID, uid).Scan(&validity, &folder)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.mail.Wake(m.Address, validity, uid, folder); err != nil {
		s.mailError(w, r, err)
		return
	}
	s.db.Exec(r.Context(), `DELETE FROM snoozed_messages WHERE mailbox_id = $1 AND uid_validity = $2 AND uid = $3`, m.ID, validity, uid)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) RunScheduler(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		for s.sendNextScheduled(ctx) {
		}
		s.wakeSnoozed(ctx)
		s.deliverWebhooks(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) sendNextScheduled(ctx context.Context) bool {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)
	var id, mailboxID, address, name, inReplyTo string
	var recipients []string
	var raw []byte
	var attempts int
	var usable bool
	err = tx.QueryRow(ctx, `
		SELECT s.id, s.mailbox_id, m.address, m.display_name, s.recipients, s.raw, s.in_reply_to, s.attempts,
		       m.active AND d.active AND d.verified_at IS NOT NULL
		FROM scheduled_messages s JOIN mailboxes m ON m.id = s.mailbox_id JOIN domains d ON d.id = m.domain_id
		WHERE s.error IS NULL AND s.send_at <= now()
		ORDER BY s.send_at LIMIT 1 FOR UPDATE OF s SKIP LOCKED`).
		Scan(&id, &mailboxID, &address, &name, &recipients, &raw, &inReplyTo, &attempts, &usable)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("loading scheduled messages failed", "err", err)
		}
		return false
	}
	if !usable {
		tx.Exec(ctx, `UPDATE scheduled_messages SET error = 'The mailbox is disabled.' WHERE id = $1`, id)
		return tx.Commit(ctx) == nil
	}
	if err := s.mail.Send(mailpkg.Address{Name: name, Address: address}, recipients, raw); err != nil {
		mailSends.WithLabelValues("rejected").Inc()
		s.log.Warn("scheduled send failed", "mailbox", mailboxID, "attempt", attempts+1, "err", err)
		if attempts+1 >= maxSendAttempts {
			tx.Exec(ctx, `UPDATE scheduled_messages SET attempts = attempts + 1, error = $2 WHERE id = $1`, id, "The mail server did not accept the message: "+err.Error())
		} else {
			tx.Exec(ctx, `UPDATE scheduled_messages SET attempts = attempts + 1, send_at = now() + make_interval(mins => 5 * (attempts + 1)) WHERE id = $1`, id)
		}
		return tx.Commit(ctx) == nil
	}
	mailSends.WithLabelValues("accepted").Inc()
	if _, err := tx.Exec(ctx, `DELETE FROM scheduled_messages WHERE id = $1`, id); err != nil {
		s.log.Error("scheduled message sent but not removed", "id", id, "err", err)
		return false
	}
	if err := tx.Commit(ctx); err != nil {
		s.log.Error("scheduled message sent but not removed", "id", id, "err", err)
		return false
	}
	if err := s.mail.SaveSent(address, raw); err != nil {
		s.log.Error("saving to Sent failed", "mailbox", mailboxID, "err", err)
	}
	if inReplyTo != "" {
		s.mail.MarkAnswered(address, inReplyTo)
	}
	return true
}

func (s *Server) wakeSnoozed(ctx context.Context) {
	rows, err := s.db.Query(ctx, `
		SELECT s.id, m.address, s.uid_validity, s.uid, s.return_folder
		FROM snoozed_messages s JOIN mailboxes m ON m.id = s.mailbox_id
		WHERE s.until <= now() ORDER BY s.until LIMIT 50`)
	if err != nil {
		s.log.Error("loading snoozed messages failed", "err", err)
		return
	}
	type due struct {
		id, address, folder string
		validity, uid       uint32
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		return d, row.Scan(&d.id, &d.address, &d.validity, &d.uid, &d.folder)
	})
	if err != nil {
		s.log.Error("loading snoozed messages failed", "err", err)
		return
	}
	for _, d := range items {
		if err := s.mail.Wake(d.address, d.validity, d.uid, d.folder); err != nil {
			s.log.Warn("waking snoozed message failed; retrying in five minutes", "err", err)
			s.db.Exec(ctx, `UPDATE snoozed_messages SET until = now() + interval '5 minutes' WHERE id = $1`, d.id)
			continue
		}
		s.db.Exec(ctx, `DELETE FROM snoozed_messages WHERE id = $1`, d.id)
	}
}

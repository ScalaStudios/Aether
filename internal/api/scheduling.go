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
	scheduleLead           = 30 * time.Second
)

type scheduledMessage struct {
	ID      string            `json:"id"`
	SendAt  time.Time         `json:"sendAt"`
	Subject string            `json:"subject"`
	To      []mailpkg.Address `json:"to"`
	Error   *string           `json:"error"`
	Undo    bool              `json:"undo"`
}

func (s *Server) readyToSchedule(w http.ResponseWriter, r *http.Request, m *ownedMailbox, sendAt time.Time, undo bool) (time.Time, bool) {
	sendAt = sendAt.UTC()
	if undo {
		if !sendAt.After(time.Now()) || sendAt.After(time.Now().Add(time.Minute)) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_send_at", "Choose a send time in the future and within a year.")
			return time.Time{}, false
		}
	} else if sendAt.Before(time.Now().Add(5*time.Second)) || sendAt.After(time.Now().Add(maxScheduleAhead)) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_send_at", "Choose a send time in the future and within a year.")
		return time.Time{}, false
	}
	var pending int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM scheduled_messages WHERE mailbox_id = $1 AND protected_reply_id IS NULL`, m.ID).Scan(&pending); err != nil {
		s.internalError(w, r, err)
		return time.Time{}, false
	}
	if pending >= maxScheduledPerMailbox {
		writeError(w, http.StatusUnprocessableEntity, "too_many_scheduled", "This mailbox already has 100 scheduled messages.")
		return time.Time{}, false
	}
	return sendAt, true
}

func (s *Server) insertScheduled(w http.ResponseWriter, r *http.Request, p *principal, m *ownedMailbox, out *mailpkg.Outgoing, raw []byte, sendAt time.Time, draftUID uint32, createdLinks []string, undo bool) {
	bcc := []string{}
	for _, a := range out.Bcc {
		bcc = append(bcc, a.Address)
	}
	to := out.To
	if to == nil {
		to = []mailpkg.Address{}
	}
	mode := "send_as"
	if !m.SendAs {
		mode = "send_on_behalf"
	}
	var item scheduledMessage
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO scheduled_messages (mailbox_id, send_at, recipients, bcc, to_addresses, subject, in_reply_to, raw, sender_user_id, sender_mode, from_address, undo)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id, send_at, subject, to_addresses, error, undo`,
		m.ID, sendAt, out.Recipients(), bcc, to, out.Subject, out.InReplyTo, raw, p.User.ID, mode, out.From.Address, undo).
		Scan(&item.ID, &item.SendAt, &item.Subject, &item.To, &item.Error, &item.Undo)
	if err != nil {
		s.deleteFileLinks(r.Context(), m.ID, createdLinks)
		s.internalError(w, r, err)
		return
	}
	if draftUID != 0 && m.Access != "read" {
		if err := s.mail.DeleteDraft(m.Address, draftUID); err != nil {
			s.log.Error("removing scheduled draft failed", "mailbox", m.ID, "err", err)
		}
	}
	if time.Until(sendAt) <= scheduleLead {
		wake(s.scheduleWake)
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) listScheduled(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, send_at, subject, to_addresses, error, undo FROM scheduled_messages
		WHERE mailbox_id = $1 AND protected_reply_id IS NULL ORDER BY send_at`, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (scheduledMessage, error) {
		var it scheduledMessage
		return it, row.Scan(&it.ID, &it.SendAt, &it.Subject, &it.To, &it.Error, &it.Undo)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) cancelScheduled(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.cancellableMailbox(w, r, p)
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
	err = tx.QueryRow(r.Context(), `DELETE FROM scheduled_messages WHERE id = $1 AND mailbox_id = $2 AND protected_reply_id IS NULL
		RETURNING raw, bcc`, id, m.ID).Scan(&raw, &bcc)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "That message is no longer waiting to be sent. It may already have gone out.")
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

func (s *Server) cancellableMailbox(w http.ResponseWriter, r *http.Request, p *principal) (*ownedMailbox, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, false
	}
	m, access, err := s.accessibleMailbox(r.Context(), id, p.User.ID)
	if err != nil {
		notFound(w)
		return nil, false
	}
	if access == "read" && !m.SendAs && !m.SendOnBehalf {
		writeError(w, http.StatusForbidden, "read_only", "You have read-only access to this mailbox.")
		return nil, false
	}
	return m, true
}

func (s *Server) undoDelay(ctx context.Context, userID string) (time.Duration, error) {
	var seconds int
	if err := s.db.QueryRow(ctx, `SELECT undo_send_seconds FROM users WHERE id = $1`, userID).Scan(&seconds); err != nil {
		return 0, err
	}
	if seconds == 0 || !allowedUndoDelay(seconds) {
		return 0, nil
	}
	return time.Duration(seconds) * time.Second, nil
}

func (s *Server) nextSendAt(ctx context.Context) (time.Time, bool) {
	var at time.Time
	err := s.db.QueryRow(ctx, `SELECT send_at FROM scheduled_messages WHERE error IS NULL AND send_at > now() ORDER BY send_at LIMIT 1`).Scan(&at)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("loading the next scheduled send failed", "err", err)
		}
		return time.Time{}, false
	}
	return at, true
}

func (s *Server) RunScheduler(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var cleaned time.Time
	var early *time.Timer
	var earlyC <-chan time.Time
	stopEarly := func() {
		if early == nil {
			return
		}
		if !early.Stop() {
			select {
			case <-early.C:
			default:
			}
		}
		early = nil
		earlyC = nil
	}
	defer stopEarly()
	var lastAuditPurge time.Time
	for {
		if _, err := s.db.Exec(ctx, `DELETE FROM protected_messages WHERE id IN (
			SELECT id FROM protected_messages WHERE expires_at <= now() ORDER BY expires_at LIMIT 100 FOR UPDATE SKIP LOCKED)`); err != nil && ctx.Err() == nil {
			s.log.Error("expiring protected messages failed", "err", err)
		}
		for s.sendNextScheduled(ctx) {
		}
		s.wakeSnoozed(ctx)
		s.dispatchImports(ctx)
		s.deliverWebhooks(ctx)
		s.sweepQuarantine(ctx)
		s.sendQuarantineDigests(ctx)
		if time.Since(cleaned) > time.Hour {
			s.cleanMailFlow(ctx)
			cleaned = time.Now()
		}
		go s.purgeRecoverable(ctx)
		s.pruneMailboxAudit(ctx)
		s.expireHeldMessages(ctx)
		s.deliverQueuedListMessages(ctx)
		stopEarly()
		if at, ok := s.nextSendAt(ctx); ok {
			wait := time.Until(at)
			if wait < 0 {
				wait = 0
			}
			if wait <= scheduleLead {
				early = time.NewTimer(wait)
				earlyC = early.C
			}
		}
		s.deliverSecurityEmails(ctx)
		if lastAuditPurge.IsZero() || time.Since(lastAuditPurge) >= auditPurgeInterval {
			s.purgeExpiredAuditEvents(ctx)
			lastAuditPurge = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-s.securityNotifyWake:
		case <-ticker.C:
		case <-s.scheduleWake:
		case <-earlyC:
		}
	}
}

func (s *Server) sendNextScheduled(ctx context.Context) bool {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)
	var id, mailboxID, orgID, address, fromAddress, sentAddress, inReplyTo, subject, mode string
	var senderID *string
	var recipients []string
	var raw []byte
	var attempts int
	var usable, permitted, writable, protectedReply bool
	err = tx.QueryRow(ctx, `
		SELECT s.id, s.mailbox_id, m.org_id, m.address, coalesce(s.from_address, m.address), coalesce(i.sent_address, m.address), s.recipients, s.raw, s.in_reply_to, s.attempts, s.subject, s.sender_mode, s.sender_user_id,
		       m.active AND 'webmail' = ANY(m.protocols) AND d.active AND d.verified_at IS NOT NULL,
		       coalesce(i.address IS NOT NULL AND u.status = 'active' AND CASE s.sender_mode WHEN 'send_as' THEN `+mailboxSendAs("s.sender_user_id")+` ELSE `+mailboxSendOnBehalf("s.sender_user_id")+` END, false),
		       `+mailboxAccessLevel("s.sender_user_id")+` IN ('owner', 'full'),
		       s.protected_reply_id IS NOT NULL
		FROM scheduled_messages s JOIN mailboxes m ON m.id = s.mailbox_id JOIN domains d ON d.id = m.domain_id LEFT JOIN users u ON u.id = s.sender_user_id
		LEFT JOIN LATERAL (`+sendableAddresses("s.sender_user_id")+`) i ON i.address = coalesce(s.from_address, m.address)
		WHERE s.error IS NULL AND s.send_at <= now()
		  AND (s.protected_reply_id IS NULL OR EXISTS (
		    SELECT 1 FROM protected_messages p WHERE p.id = s.protected_reply_id AND p.expires_at > now()))
		ORDER BY s.send_at LIMIT 1 FOR UPDATE OF s SKIP LOCKED`).
		Scan(&id, &mailboxID, &orgID, &address, &fromAddress, &sentAddress, &recipients, &raw, &inReplyTo, &attempts, &subject, &mode, &senderID, &usable, &permitted, &writable, &protectedReply)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("loading scheduled messages failed", "err", err)
		}
		return false
	}
	if !usable {
		tx.Exec(ctx, `UPDATE scheduled_messages SET error = 'The mailbox is disabled or Aether Mail is turned off for it.' WHERE id = $1`, id)
		return tx.Commit(ctx) == nil
	}
	if !permitted {
		tx.Exec(ctx, `UPDATE scheduled_messages SET error = 'You no longer have permission to send from this mailbox.' WHERE id = $1`, id)
		return tx.Commit(ctx) == nil
	}
	if !protectedReply {
		protocol, encrypted := mailpkg.ClassifyRaw(raw)
		denial, err := s.encryptionBlock(ctx, orgID, protocol, encrypted, recipients)
		if err != nil {
			s.log.Error("checking encryption policy failed", "err", err)
			return false
		}
		if denial != "" {
			tx.Exec(ctx, `UPDATE scheduled_messages SET error = $2 WHERE id = $1`, id, denial)
			return tx.Commit(ctx) == nil
		}
	}
	if err := s.mail.Send(address, fromAddress, recipients, raw); err != nil {
		mailSends.WithLabelValues("rejected").Inc()
		s.recordAlertEvent(ctx, "delivery_failures", address)
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
	if !protectedReply {
		if err := s.mail.SaveSent(sentAddress, raw); err != nil {
			s.log.Error("saving to Sent failed", "mailbox", mailboxID, "err", err)
		}
	}
	if senderID != nil {
		s.recordScheduledSend(ctx, mailboxID, *senderID, mode, subject, raw, len(recipients))
	}
	if inReplyTo != "" && writable {
		s.mail.MarkAnswered(address, inReplyTo)
	}
	return true
}

func (s *Server) wakeSnoozed(ctx context.Context) {
	rows, err := s.db.Query(ctx, `
		UPDATE snoozed_messages s SET until = now() + interval '5 minutes'
		FROM mailboxes m
		WHERE s.id IN (SELECT id FROM snoozed_messages WHERE until <= now() ORDER BY until LIMIT 50 FOR UPDATE SKIP LOCKED)
		  AND m.id = s.mailbox_id
		RETURNING s.id, m.address, s.uid_validity, s.uid, s.return_folder`)
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
			continue
		}
		s.db.Exec(ctx, `DELETE FROM snoozed_messages WHERE id = $1`, d.id)
	}
}

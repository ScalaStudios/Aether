package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
	"github.com/emersion/go-smtp"
	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	moderationHold  = 5 * 24 * time.Hour
	maxListAttempts = 30
	maxListMessage  = 50 << 20
	listHeader      = "X-Aether-List"
)

type moderatedList struct {
	ID           string
	OrgID        string
	Address      string
	Name         string
	Destinations []string
	Moderated    bool
	Exempt       []string
}

type heldMessage struct {
	ID          string    `json:"id"`
	ListID      string    `json:"listId"`
	ListAddress string    `json:"listAddress"`
	ListName    string    `json:"listName"`
	Sender      string    `json:"sender"`
	From        string    `json:"from"`
	Subject     string    `json:"subject"`
	Preview     string    `json:"preview"`
	Size        int       `json:"size"`
	ReceivedAt  time.Time `json:"receivedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func listModerator(aliasExpr, userExpr string) string {
	return `(EXISTS (SELECT 1 FROM alias_moderators lm WHERE lm.alias_id = ` + aliasExpr + ` AND lm.user_id = ` + userExpr + `)
		OR (NOT EXISTS (SELECT 1 FROM alias_moderators lm WHERE lm.alias_id = ` + aliasExpr + `)
			AND EXISTS (SELECT 1 FROM alias_owners lo WHERE lo.alias_id = ` + aliasExpr + ` AND lo.user_id = ` + userExpr + `)))`
}

func listRecipient(rcpt string) (string, bool) {
	local, domain, ok := strings.Cut(strings.ToLower(rcpt), "@")
	if !ok || domain != "aether-reports.internal" {
		return "", false
	}
	local, _, _ = strings.Cut(local, "+")
	id, ok := strings.CutPrefix(local, "list-")
	return id, ok && uuidPattern.MatchString(id)
}

func srsOriginal(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 || !strings.HasPrefix(strings.ToUpper(addr), "SRS0=") {
		return strings.ToLower(addr)
	}
	parts := strings.SplitN(addr[:at], "=", 5)
	if len(parts) != 5 || parts[3] == "" || parts[4] == "" {
		return strings.ToLower(addr)
	}
	return strings.ToLower(parts[4] + "@" + parts[3])
}

func isExempt(sender string, exempt []string) bool {
	_, domain, ok := strings.Cut(sender, "@")
	return ok && (slices.Contains(exempt, sender) || slices.Contains(exempt, "@"+domain))
}

func (s *Server) loadModeratedList(ctx context.Context, id string) (*moderatedList, error) {
	l := &moderatedList{ID: id}
	err := s.db.QueryRow(ctx, `
		WITH people AS (SELECT user_id FROM alias_owners WHERE alias_id = $1 UNION SELECT user_id FROM alias_moderators WHERE alias_id = $1)
		SELECT a.org_id, a.address, a.name,
		       ARRAY(SELECT unnest(a.destinations) UNION SELECT lm.address FROM list_dynamic_members lm WHERE lm.alias_id = a.id),
		       a.active AND a.moderated,
		       a.moderation_exempt || coalesce(ARRAY(
		           SELECT m.address FROM mailboxes m WHERE m.owner_user_id IN (SELECT user_id FROM people)
		           UNION SELECT u.email FROM users u WHERE u.id IN (SELECT user_id FROM people)), '{}')
		FROM aliases a WHERE a.id = $1`, id).
		Scan(&l.OrgID, &l.Address, &l.Name, &l.Destinations, &l.Moderated, &l.Exempt)
	return l, err
}

func (s *Server) receiveListMessage(aliasID, envelopeFrom string, raw []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err := s.handleListMessage(ctx, aliasID, envelopeFrom, raw)
	if err != nil {
		s.log.Error("handling a message for a moderated list failed", "list", aliasID, "err", err)
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Try again later"}
	}
	return nil
}

func (s *Server) handleListMessage(ctx context.Context, aliasID, envelopeFrom string, raw []byte) error {
	l, err := s.loadModeratedList(ctx, aliasID)
	if errors.Is(err, pgx.ErrNoRows) {
		s.log.Warn("dropped a message for a list that no longer exists", "list", aliasID)
		return nil
	}
	if err != nil {
		return err
	}
	h, _ := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	for _, v := range h.Values(listHeader) {
		if strings.EqualFold(strings.TrimSpace(v), l.Address) {
			s.log.Warn("dropped a message that looped back to its list", "list", l.Address)
			return nil
		}
	}
	sender := srsOriginal(envelopeFrom)
	exempt := isExempt(sender, l.Exempt)
	if l.Moderated && exempt && sender != strings.ToLower(envelopeFrom) {
		_, domain, _ := strings.Cut(sender, "@")
		if err := s.db.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM mail_domains WHERE name = $1)`, domain).Scan(&exempt); err != nil {
			return err
		}
	}
	mh := mail.Header{Header: message.Header{Header: h}}
	subject, _ := mh.Subject()
	from := h.Get("From")
	if addrs, err := mh.AddressList("From"); err == nil && len(addrs) > 0 {
		from = addrs[0].String()
	}
	held := heldMessage{ListID: l.ID, ListAddress: l.Address, ListName: l.Name, Sender: envelopeFrom, From: truncate(from, 500), Subject: truncate(subject, 500), Size: len(raw)}
	if !l.Moderated || exempt {
		if err := s.db.QueryRow(ctx, `INSERT INTO held_messages (alias_id, sender, from_header, subject, raw, expires_at, approved_at) VALUES ($1, $2, $3, $4, $5, now(), now()) RETURNING id`,
			l.ID, held.Sender, held.From, held.Subject, raw).Scan(&held.ID); err != nil {
			return err
		}
		if err := s.deliverListMessage(ctx, held.ID); err != nil {
			s.log.Warn("list delivery incomplete; it will be retried", "list", l.Address, "message", held.ID, "err", err)
		}
		return nil
	}
	held.Preview = mailpkg.TextPreview(raw, 2000)
	err = s.db.QueryRow(ctx, `INSERT INTO held_messages (alias_id, sender, from_header, subject, preview, raw, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, received_at, expires_at`,
		l.ID, held.Sender, held.From, held.Subject, held.Preview, raw, time.Now().Add(moderationHold)).Scan(&held.ID, &held.ReceivedAt, &held.ExpiresAt)
	if err != nil {
		return err
	}
	if err := s.notifyModerators(ctx, l, &held); err != nil {
		s.log.Error("notifying list moderators failed", "list", l.Address, "held", held.ID, "err", err)
	}
	return nil
}

func (s *Server) deliverListMessage(ctx context.Context, id string) error {
	var aliasID, sender, subject string
	var raw []byte
	var delivered []string
	var attempts int
	err := s.db.QueryRow(ctx, `
		UPDATE held_messages SET claimed_until = now() + interval '15 minutes', attempts = attempts + 1
		WHERE id = $1 AND approved_at IS NOT NULL AND (claimed_until IS NULL OR claimed_until < now())
		RETURNING alias_id, sender, subject, raw, delivered, attempts`, id).Scan(&aliasID, &sender, &subject, &raw, &delivered, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	l, err := s.loadModeratedList(ctx, aliasID)
	if err == nil {
		rcpts := slices.DeleteFunc(l.Destinations, func(d string) bool { return d == l.Address || slices.Contains(delivered, d) })
		err = s.mail.Relay(sender, rcpts, append([]byte(listHeader+": "+l.Address+"\r\n"), raw...), func(accepted, rejected []string) error {
			if len(rejected) > 0 {
				s.log.Warn("the mail server refused list members", "list", l.Address, "message", id, "members", rejected)
			}
			_, err := s.db.Exec(ctx, `UPDATE held_messages SET delivered = delivered || $2 WHERE id = $1`, id, append(accepted, rejected...))
			return err
		})
	}
	if err == nil || attempts >= maxListAttempts {
		if _, dbErr := s.db.Exec(ctx, `DELETE FROM held_messages WHERE id = $1`, id); dbErr != nil {
			return dbErr
		}
		if err != nil && l.Address != "" {
			s.log.Error("gave up delivering a list message", "list", l.Address, "message", id, "err", err)
			s.noticeSender(l, sender, subject, "Your message to "+l.Address+" could not be delivered to every member of the list.\r\n")
		}
		return err
	}
	if _, dbErr := s.db.Exec(ctx, `UPDATE held_messages SET claimed_until = NULL, next_attempt_at = now() + make_interval(mins => least(5 * $2, 60)) WHERE id = $1`, id, attempts); dbErr != nil {
		s.log.Error("scheduling a list delivery retry failed", "message", id, "err", dbErr)
	}
	return err
}

func (s *Server) deliverQueuedListMessages(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT id FROM held_messages WHERE approved_at IS NOT NULL AND next_attempt_at <= now() AND (claimed_until IS NULL OR claimed_until < now()) ORDER BY next_attempt_at LIMIT 20`)
	if err != nil {
		s.log.Error("loading queued list messages failed", "err", err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.log.Error("loading queued list messages failed", "err", err)
		return
	}
	for _, id := range ids {
		if err := s.deliverListMessage(ctx, id); err != nil {
			s.log.Warn("list delivery incomplete; it will be retried", "message", id, "err", err)
		}
	}
}

func (s *Server) sendNotice(l *moderatedList, to []mailpkg.Address, subject, text string) error {
	if len(to) == 0 {
		return nil
	}
	out := &mailpkg.Outgoing{From: mailpkg.Address{Name: l.Name, Address: l.Address}, To: to, Subject: subject, Text: text}
	raw, _, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		return err
	}
	return s.mail.Relay("", out.Recipients(), raw, nil)
}

func (s *Server) notifyModerators(ctx context.Context, l *moderatedList, m *heldMessage) error {
	rows, err := s.db.Query(ctx, `SELECT u.display_name, u.email FROM users u WHERE u.status = 'active' AND `+listModerator("$1", "u.id"), l.ID)
	if err != nil {
		return err
	}
	to, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mailpkg.Address])
	if err != nil {
		return err
	}
	link := strings.TrimRight(s.cfg.PublicURL.String(), "/") + "/settings?held=" + m.ID
	text := "A message to " + l.Address + " is waiting for your approval.\r\n\r\n" +
		"From: " + m.From + "\r\n" +
		"Subject: " + m.Subject + "\r\n" +
		"Held until: " + m.ExpiresAt.UTC().Format("2 January 2006 15:04 MST") + "\r\n\r\n" +
		"Approve: " + link + "&decision=approve\r\n" +
		"Reject: " + link + "&decision=reject\r\n\r\n" +
		"If nobody decides by then, the message is discarded and the sender is told.\r\n"
	return s.sendNotice(l, to, "Approval needed: "+m.Subject, text)
}

func (s *Server) noticeSender(l *moderatedList, sender, subject, text string) {
	if sender == "" {
		return
	}
	if err := s.sendNotice(l, []mailpkg.Address{{Address: sender}}, "Not delivered: "+subject, text); err != nil {
		s.log.Warn("telling the sender about a held message failed", "list", l.Address, "err", err)
	}
}

func (s *Server) myHeldMessages(w http.ResponseWriter, r *http.Request, p *principal) {
	rows, err := s.db.Query(r.Context(), `
		SELECT h.id, a.id, a.address, a.name, h.sender, h.from_header, h.subject, h.preview, length(h.raw), h.received_at, h.expires_at
		FROM held_messages h JOIN aliases a ON a.id = h.alias_id
		JOIN organization_members om ON om.org_id = a.org_id AND om.user_id = $1
		WHERE h.approved_at IS NULL AND `+listModerator("a.id", "$1")+` ORDER BY h.received_at`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[heldMessage])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) heldFromPath(w http.ResponseWriter, r *http.Request, p *principal, tx pgx.Tx) (*heldMessage, []byte, bool) {
	id, ok := pathID(w, r, "heldId")
	if !ok {
		return nil, nil, false
	}
	var m heldMessage
	var raw []byte
	var orgID string
	var moderator bool
	err := tx.QueryRow(r.Context(), `
		SELECT h.id, a.id, a.address, a.name, h.sender, h.from_header, h.subject, h.raw, a.org_id, `+listModerator("a.id", "$2")+`
		FROM held_messages h JOIN aliases a ON a.id = h.alias_id WHERE h.id = $1 AND h.approved_at IS NULL FOR UPDATE OF h`, id, p.User.ID).
		Scan(&m.ID, &m.ListID, &m.ListAddress, &m.ListName, &m.Sender, &m.From, &m.Subject, &raw, &orgID, &moderator)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!moderator || p.orgRole(orgID) == "")) {
		writeError(w, http.StatusNotFound, "not_found", "This message is no longer waiting for approval. Another moderator may have decided, or it expired.")
		return nil, nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	return &m, raw, true
}

func (s *Server) heldMessageRaw(w http.ResponseWriter, r *http.Request, p *principal) {
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	m, raw, ok := s.heldFromPath(w, r, p, tx)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", mailpkg.ContentDisposition("held-"+m.ID+".eml"))
	w.Write(raw)
}

func (s *Server) decideHeldMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	approve := r.PathValue("decision") == "approve"
	if !approve && r.PathValue("decision") != "reject" {
		notFound(w)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !approve && r.ContentLength != 0 && !decode(w, r, 16<<10, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len(in.Reason) > 2000 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_reason", "Keep the reason under 2,000 characters.")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	m, _, ok := s.heldFromPath(w, r, p, tx)
	if !ok {
		return
	}
	l, err := s.loadModeratedList(r.Context(), m.ListID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	query := `DELETE FROM held_messages WHERE id = $1`
	if approve {
		query = `UPDATE held_messages SET approved_at = now(), next_attempt_at = now() WHERE id = $1`
	}
	if _, err := tx.Exec(r.Context(), query, m.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.internalError(w, r, err)
		return
	}
	meta := map[string]any{"address": l.Address, "heldMessageId": m.ID, "sender": m.Sender, "from": m.From, "subject": m.Subject}
	if approve {
		s.audit(r.Context(), r, p, &l.OrgID, "alias.message_approve", "alias", l.ID, meta)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
		defer cancel()
		if err := s.deliverListMessage(ctx, m.ID); err != nil {
			s.log.Warn("list delivery incomplete; it will be retried", "list", l.Address, "message", m.ID, "err", err)
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
			return
		}
	} else {
		meta["reason"] = in.Reason
		s.audit(r.Context(), r, p, &l.OrgID, "alias.message_reject", "alias", l.ID, meta)
		text := "Your message to " + l.Address + " was not delivered because a moderator of the list rejected it.\r\n"
		if in.Reason != "" {
			text += "\r\nReason: " + in.Reason + "\r\n"
		}
		s.noticeSender(l, m.Sender, m.Subject, text)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) expireHeldMessages(ctx context.Context) {
	rows, err := s.db.Query(ctx, `
		DELETE FROM held_messages WHERE id IN (SELECT id FROM held_messages WHERE approved_at IS NULL AND expires_at <= now() ORDER BY expires_at LIMIT 100 FOR UPDATE SKIP LOCKED)
		RETURNING alias_id, sender, subject`)
	if err != nil {
		s.log.Error("expiring held list messages failed", "err", err)
		return
	}
	type expired struct{ ListID, Sender, Subject string }
	gone, err := pgx.CollectRows(rows, pgx.RowToStructByPos[expired])
	if err != nil {
		s.log.Error("expiring held list messages failed", "err", err)
		return
	}
	for _, e := range gone {
		l, err := s.loadModeratedList(ctx, e.ListID)
		if err != nil {
			continue
		}
		s.noticeSender(l, e.Sender, e.Subject, "Your message to "+l.Address+" was not delivered because no moderator of the list approved it within 5 days.\r\n")
	}
}

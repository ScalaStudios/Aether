package api

import (
	"errors"
	"html"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

const protectedNoticeSubject = "Password-protected message"

var protectedToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

type protectedMessage struct {
	Sender    string    `json:"sender"`
	Recipient string    `json:"recipient"`
	Salt      []byte    `json:"salt"`
	Message   []byte    `json:"message"`
	Reply     []byte    `json:"reply,omitempty"`
	Replied   bool      `json:"replied"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func validProtectedCiphertext(value []byte) bool {
	return len(value) >= 29 && len(value) <= 256<<10
}

func (s *Server) protectedURL(query url.Values, fragment string) string {
	u := *s.cfg.PublicURL
	u.Path, u.RawPath, u.RawQuery, u.Fragment, u.RawFragment = "/protected", "", query.Encode(), fragment, ""
	return u.String()
}

func (s *Server) sendProtectedMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.sendingMailbox(w, r, p)
	if !ok {
		return
	}
	if m.Kind != "user" || m.Access != "owner" {
		forbidden(w)
		return
	}
	host := s.cfg.PublicURL.Hostname()
	ip := net.ParseIP(host)
	if !s.cfg.SecureCookies() && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		writeError(w, http.StatusServiceUnavailable, "https_required", "Password-protected mail requires an HTTPS web address.")
		return
	}
	ctx := r.Context()
	if !s.allow(ctx, "send:"+m.ID, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "This mailbox is sending too quickly. Wait a minute before sending again.")
		return
	}
	var in struct {
		ID        string          `json:"id"`
		To        mailpkg.Address `json:"to"`
		Access    string          `json:"access"`
		ReplyHash []byte          `json:"replyHash"`
		Salt      []byte          `json:"salt"`
		Message   []byte          `json:"message"`
		Hours     int             `json:"hours"`
	}
	if !decode(w, r, 360<<10, &in) {
		return
	}
	rcpts, valid := cleanAddresses([]mailpkg.Address{in.To})
	if !valid || !uuidPattern.MatchString(in.ID) || !protectedToken.MatchString(in.Access) || len(in.ReplyHash) != 32 ||
		len(in.Salt) != 16 || !validProtectedCiphertext(in.Message) || (in.Hours != 24 && in.Hours != 72 && in.Hours != 168) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_protected_message", "Check the recipient, message size, and expiry.")
		return
	}
	recipient := rcpts[0].Address
	if !s.allowEncryption(w, r, m.OrgID, "password", true, false, []string{recipient}) {
		return
	}
	accessHash := secrets.SHA256(in.Access)
	tag, err := s.db.Exec(ctx, `
		INSERT INTO protected_messages (id, mailbox_id, sender_user_id, recipient, access_hash, reply_hash, salt, message, expires_at)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, now() + make_interval(hours => $9)
		FROM organizations WHERE id = $10 AND password_policy <> 'off'
		ON CONFLICT (id) DO NOTHING`, in.ID, m.ID, p.User.ID, recipient, accessHash, in.ReplyHash, in.Salt, in.Message, in.Hours, m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	var expires time.Time
	var sent bool
	err = tx.QueryRow(ctx, `
		SELECT expires_at, sent_at IS NOT NULL FROM protected_messages
		WHERE id = $1 AND mailbox_id = $2 AND sender_user_id = $3 AND recipient = $4 AND access_hash = $5 AND message = $6
			AND expires_at > now()
		FOR UPDATE`, in.ID, m.ID, p.User.ID, recipient, accessHash, in.Message).Scan(&expires, &sent)
	if errors.Is(err, pgx.ErrNoRows) && tag.RowsAffected() == 0 {
		writeError(w, http.StatusForbidden, "protected_mail_unavailable", "Password-protected mail is unavailable for this request.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	query := url.Values{"mailbox": {m.ID}, "message": {in.ID}}
	senderURL := s.protectedURL(query, "")
	if !sent {
		var policy string
		if err := tx.QueryRow(ctx, `SELECT password_policy FROM organizations WHERE id = $1`, m.OrgID).Scan(&policy); err != nil {
			s.internalError(w, r, err)
			return
		}
		if policy == "off" {
			writeError(w, http.StatusForbidden, "protected_mail_disabled", "Your organization has turned off password-protected mail.")
			return
		}
		out := &mailpkg.Outgoing{From: mailpkg.Address{Name: m.DisplayName, Address: m.Address}, To: rcpts, Subject: protectedNoticeSubject}
		openURL := s.protectedURL(nil, in.ID+"."+in.Access)
		protectedNotice(out, "Use the password shared by the sender to open this message.", "Open protected message", openURL, expires)
		raw, _, err := out.Build(s.cfg.MailHostname)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if err := s.mail.Send(out.From.Address, out.From.Address, []string{recipient}, raw); err != nil {
			mailSends.WithLabelValues("rejected").Inc()
			s.log.Warn("protected message submission failed", "mailbox", m.ID, "err", err)
			writeError(w, http.StatusBadGateway, "send_failed", "The mail server did not confirm delivery. Try sending again.")
			return
		}
		mailSends.WithLabelValues("accepted").Inc()
		if _, err := tx.Exec(ctx, `UPDATE protected_messages SET sent_at = now() WHERE id = $1`, in.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
		protectedNotice(out, "Open your copy with the original password. A reply will appear here when the recipient sends one.", "Open your copy", senderURL, expires)
		raw, _, err = out.Build(s.cfg.MailHostname)
		if err == nil {
			err = s.mail.SaveSent(m.Address, raw)
		}
		if err != nil {
			s.log.Error("saving protected message to Sent failed", "mailbox", m.ID, "err", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"url": senderURL, "expiresAt": expires})
}

func protectedNotice(out *mailpkg.Outgoing, description, label, link string, expires time.Time) {
	until := "This link expires " + expires.UTC().Format("2 January 2006 at 15:04 UTC") + "."
	out.Text = description + "\r\n\r\n" + link + "\r\n\r\n" + until
	out.HTML = mailpkg.SanitizeHTML("<p>" + html.EscapeString(description) + `</p><p><a href="` + html.EscapeString(link) +
		`">` + html.EscapeString(label) + "</a></p><p>" + until + "</p>")
}

func (s *Server) protectedRecipient(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "messageId")
	if !ok {
		return
	}
	ctx := r.Context()
	if !s.allow(ctx, "protected:"+s.clientIP(r), 60) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Wait a minute before trying again.")
		return
	}
	var in struct {
		Access string `json:"access"`
	}
	if !decode(w, r, 1024, &in) {
		return
	}
	if !protectedToken.MatchString(in.Access) {
		notFound(w)
		return
	}
	var item protectedMessage
	err := s.db.QueryRow(ctx, `
		SELECT m.address, p.recipient, p.salt, p.message, p.reply IS NOT NULL, p.expires_at
		FROM protected_messages p JOIN mailboxes m ON m.id = p.mailbox_id
		WHERE p.id = $1 AND p.access_hash = $2 AND p.sent_at IS NOT NULL AND p.expires_at > now()`, id, secrets.SHA256(in.Access)).
		Scan(&item.Sender, &item.Recipient, &item.Salt, &item.Message, &item.Replied, &item.ExpiresAt)
	s.writeProtectedMessage(w, r, item, err)
}

func (s *Server) protectedSender(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "messageId")
	if !ok {
		return
	}
	var item protectedMessage
	err := s.db.QueryRow(r.Context(), `
		SELECT recipient, salt, message, reply, reply IS NOT NULL, expires_at FROM protected_messages
		WHERE id = $1 AND mailbox_id = $2 AND sender_user_id = $3 AND sent_at IS NOT NULL AND expires_at > now()`, id, m.ID, p.User.ID).
		Scan(&item.Recipient, &item.Salt, &item.Message, &item.Reply, &item.Replied, &item.ExpiresAt)
	item.Sender = m.Address
	s.writeProtectedMessage(w, r, item, err)
}

func (s *Server) writeProtectedMessage(w http.ResponseWriter, r *http.Request, item protectedMessage, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "protected_message_unavailable", "This message link has expired or is unavailable.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) replyProtectedMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "messageId")
	if !ok {
		return
	}
	ctx := r.Context()
	if !s.allow(ctx, "protected-reply:"+s.clientIP(r), 20) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Wait a minute before trying again.")
		return
	}
	var in struct {
		Access string `json:"access"`
		Proof  string `json:"proof"`
		Reply  []byte `json:"reply"`
	}
	if !decode(w, r, 360<<10, &in) {
		return
	}
	if !protectedToken.MatchString(in.Access) || !protectedToken.MatchString(in.Proof) || !validProtectedCiphertext(in.Reply) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_protected_reply", "This reply could not be accepted.")
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	var mailboxID, userID string
	var sender mailpkg.Address
	var expires time.Time
	err = tx.QueryRow(ctx, `
		UPDATE protected_messages p SET reply = $4 FROM mailboxes m
		WHERE p.id = $1 AND p.access_hash = $2 AND p.reply_hash = $3 AND p.reply IS NULL
			AND p.sent_at IS NOT NULL AND p.expires_at > now() AND m.id = p.mailbox_id
		RETURNING p.mailbox_id, p.sender_user_id, m.address, m.display_name, p.expires_at`,
		id, secrets.SHA256(in.Access), secrets.SHA256(in.Proof), in.Reply).
		Scan(&mailboxID, &userID, &sender.Address, &sender.Name, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "protected_reply_unavailable", "This message is unavailable or already has a reply.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := &mailpkg.Outgoing{From: sender, To: []mailpkg.Address{sender}, Subject: "Reply to your password-protected message"}
	link := s.protectedURL(url.Values{"mailbox": {mailboxID}, "message": {id}}, "")
	protectedNotice(out, "Your recipient replied. Open your copy with the original password to read it.", "Read protected reply", link, expires)
	raw, _, err := out.Build(s.cfg.MailHostname)
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO scheduled_messages (mailbox_id, send_at, recipients, raw, sender_user_id, sender_mode, protected_reply_id)
			VALUES ($1, now(), $2, $3, $4, 'send_as', $5)`, mailboxID, out.Recipients(), raw, userID, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

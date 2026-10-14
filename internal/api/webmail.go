package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type ownedMailbox struct {
	ID          string
	OrgID       string
	Address     string
	DisplayName string
}

func (s *Server) ownedMailbox(w http.ResponseWriter, r *http.Request, p *principal) (*ownedMailbox, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, false
	}
	m := &ownedMailbox{}
	err := s.db.QueryRow(r.Context(), `
		SELECT m.id, m.org_id, m.address, m.display_name FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.id = $1 AND m.owner_user_id = $2 AND m.active AND d.active AND d.verified_at IS NOT NULL`, id, p.User.ID).
		Scan(&m.ID, &m.OrgID, &m.Address, &m.DisplayName)
	if err != nil {
		notFound(w)
		return nil, false
	}
	return m, true
}

func folderParam(r *http.Request) string {
	if f := r.URL.Query().Get("folder"); f != "" {
		return f
	}
	return "INBOX"
}

func uidParam(w http.ResponseWriter, r *http.Request) (uint32, bool) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil || uid == 0 {
		notFound(w)
		return 0, false
	}
	return uint32(uid), true
}

func (s *Server) mailError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, mailpkg.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "The folder or message no longer exists. It may have been moved or deleted from another client.")
		return
	}
	s.log.Error("mail backend request failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusBadGateway, "mail_unavailable", "The mail server did not respond as expected. Try again in a moment.")
}

func (s *Server) listFolders(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	folders, err := s.mail.Folders(m.Address)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(folders))
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}
	before, _ := strconv.ParseUint(q.Get("before"), 10, 32)
	page, err := s.mail.List(m.Address, folderParam(r), q.Get("q"), uint32(before), limit)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	msg, err := s.mail.Get(m.Address, folderParam(r), uid)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (s *Server) updateMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Seen    *bool `json:"seen"`
		Flagged *bool `json:"flagged"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if err := s.mail.SetFlags(m.Address, folderParam(r), uid, in.Seen, in.Flagged); err != nil {
		s.mailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	if err := s.mail.Delete(m.Address, folderParam(r), uid); err != nil {
		s.mailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) moveMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if !decode(w, r, 1<<10, &in) || in.To == "" {
		if in.To == "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "Choose a destination folder.")
		}
		return
	}
	if err := s.mail.Move(m.Address, folderParam(r), uid, in.To); err != nil {
		s.mailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	var part []int
	for _, seg := range strings.Split(r.PathValue("part"), ".") {
		n, err := strconv.Atoi(seg)
		if err != nil || n <= 0 || len(part) > 20 {
			notFound(w)
			return
		}
		part = append(part, n)
	}
	a, err := s.mail.Attachment(m.Address, folderParam(r), uid, part)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mailpkg.ContentDisposition(a.Filename))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Length", strconv.Itoa(len(a.Body)))
	w.Write(a.Body)
}

func cleanAddresses(in []mailpkg.Address) ([]mailpkg.Address, bool) {
	out := make([]mailpkg.Address, 0, len(in))
	for _, a := range in {
		parsed, err := mail.ParseAddress(strings.TrimSpace(a.Address))
		if err != nil || parsed.Name != "" || strings.ContainsAny(a.Name, "\r\n") {
			return nil, false
		}
		out = append(out, mailpkg.Address{Name: strings.TrimSpace(a.Name), Address: parsed.Address})
	}
	return out, true
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	if !s.allow(r.Context(), "send:"+m.ID, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "This mailbox is sending too quickly. Wait a minute before sending again.")
		return
	}
	var in struct {
		To         []mailpkg.Address `json:"to"`
		Cc         []mailpkg.Address `json:"cc"`
		Bcc        []mailpkg.Address `json:"bcc"`
		Subject    string            `json:"subject"`
		Text       string            `json:"text"`
		HTML       string            `json:"html"`
		InReplyTo  string            `json:"inReplyTo"`
		References []string          `json:"references"`
	}
	if !decode(w, r, 10<<20, &in) {
		return
	}
	out := mailpkg.Outgoing{From: mailpkg.Address{Name: m.DisplayName, Address: m.Address}, Subject: in.Subject, Text: in.Text, InReplyTo: in.InReplyTo}
	var ok1, ok2, ok3 bool
	out.To, ok1 = cleanAddresses(in.To)
	out.Cc, ok2 = cleanAddresses(in.Cc)
	out.Bcc, ok3 = cleanAddresses(in.Bcc)
	switch {
	case !ok1 || !ok2 || !ok3:
		writeError(w, http.StatusUnprocessableEntity, "invalid_recipient", "One of the recipient addresses is not valid.")
		return
	case len(out.To) == 0:
		writeError(w, http.StatusUnprocessableEntity, "no_recipients", "Add at least one recipient.")
		return
	case len(out.Recipients()) > 100:
		writeError(w, http.StatusUnprocessableEntity, "too_many_recipients", "Send to 100 recipients or fewer per message.")
		return
	case strings.ContainsAny(in.Subject, "\r\n") || (in.InReplyTo != "" && !validMessageID(in.InReplyTo)):
		writeError(w, http.StatusUnprocessableEntity, "invalid_header", "The subject or reply headers contain invalid characters.")
		return
	}
	for _, ref := range in.References {
		if validMessageID(ref) {
			out.References = append(out.References, ref)
		}
	}
	if in.HTML != "" {
		out.HTML = mailpkg.SanitizeHTML(in.HTML)
	}
	raw, messageID, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.mail.Send(out.From, out.Recipients(), raw); err != nil {
		s.log.Warn("submission rejected", "mailbox", m.ID, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The mail server did not accept the message: "+err.Error())
		return
	}
	if err := s.mail.SaveSent(m.Address, raw); err != nil {
		s.log.Error("saving to Sent failed", "mailbox", m.ID, "err", err)
	}
	if out.InReplyTo != "" {
		s.mail.MarkAnswered(m.Address, out.InReplyTo)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"messageId": messageID})
}

func validMessageID(v string) bool {
	return len(v) > 2 && len(v) < 998 && strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") && !strings.ContainsAny(v[1:len(v)-1], "<> \r\n\t")
}

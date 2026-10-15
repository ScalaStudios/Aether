package api

import (
	"errors"
	"mime"
	"net/http"
	"net/mail"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

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
	var access string
	err := s.db.QueryRow(r.Context(), `
		SELECT m.id, m.org_id, m.address, m.display_name, `+mailboxAccessLevel("$2")+` FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.id = $1 AND `+mailboxAccess("$2")+` AND m.active AND d.active AND d.verified_at IS NOT NULL`, id, p.User.ID).
		Scan(&m.ID, &m.OrgID, &m.Address, &m.DisplayName, &access)
	if err != nil {
		notFound(w)
		return nil, false
	}
	if access == "read" && r.Method != http.MethodGet {
		writeError(w, http.StatusForbidden, "read_only", "You have read-only access to this mailbox.")
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
	mailBackendErrs.Inc()
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
	page, err := s.mail.List(m.Address, folderParam(r), q.Get("q"), uint32(before), limit, q.Get("threads") == "true")
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
	var inv *invitation
	if msg.Calendar != "" && len(msg.From) > 0 {
		if ctx, err := s.pimContext(r, p); err == nil {
			inv = s.describeInvitation(ctx, p.User, m, strings.ToLower(msg.From[0].Address), msg.Calendar)
		}
	}
	related, err := s.mail.Related(m.Address, folderParam(r), msg)
	if err != nil {
		s.log.Warn("finding related messages failed", "mailbox", m.ID, "err", err)
	}
	writeJSON(w, http.StatusOK, struct {
		*mailpkg.Message
		Invitation *invitation       `json:"invitation,omitempty"`
		Related    []mailpkg.Related `json:"related"`
	}{msg, inv, nonNil(related)})
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

type outgoingRequest struct {
	To          []mailpkg.Address `json:"to"`
	Cc          []mailpkg.Address `json:"cc"`
	Bcc         []mailpkg.Address `json:"bcc"`
	Subject     string            `json:"subject"`
	Text        string            `json:"text"`
	HTML        string            `json:"html"`
	InReplyTo   string            `json:"inReplyTo"`
	References  []string          `json:"references"`
	Attachments []struct {
		Filename    string `json:"filename"`
		ContentType string `json:"contentType"`
		Data        []byte `json:"data"`
	} `json:"attachments"`
	ForwardFrom *struct {
		Folder string   `json:"folder"`
		UID    uint32   `json:"uid"`
		Parts  []string `json:"parts"`
	} `json:"forwardFrom"`
	DraftUID uint32     `json:"draftUid"`
	SendAt   *time.Time `json:"sendAt"`
}

func (s *Server) buildOutgoing(w http.ResponseWriter, r *http.Request, m *ownedMailbox, in *outgoingRequest, requireRecipients bool) (*mailpkg.Outgoing, bool) {
	out := &mailpkg.Outgoing{From: mailpkg.Address{Name: m.DisplayName, Address: m.Address}, Subject: in.Subject, Text: in.Text, InReplyTo: in.InReplyTo}
	var ok1, ok2, ok3 bool
	out.To, ok1 = cleanAddresses(in.To)
	out.Cc, ok2 = cleanAddresses(in.Cc)
	out.Bcc, ok3 = cleanAddresses(in.Bcc)
	switch {
	case !ok1 || !ok2 || !ok3:
		writeError(w, http.StatusUnprocessableEntity, "invalid_recipient", "One of the recipient addresses is not valid.")
		return nil, false
	case requireRecipients && len(out.To) == 0:
		writeError(w, http.StatusUnprocessableEntity, "no_recipients", "Add at least one recipient.")
		return nil, false
	case len(out.Recipients()) > 100:
		writeError(w, http.StatusUnprocessableEntity, "too_many_recipients", "Send to 100 recipients or fewer per message.")
		return nil, false
	case strings.ContainsAny(in.Subject, "\r\n") || (in.InReplyTo != "" && !validMessageID(in.InReplyTo)):
		writeError(w, http.StatusUnprocessableEntity, "invalid_header", "The subject or reply headers contain invalid characters.")
		return nil, false
	}
	for _, ref := range in.References {
		if validMessageID(ref) {
			out.References = append(out.References, ref)
		}
	}
	if in.HTML != "" {
		out.HTML = mailpkg.SanitizeHTML(in.HTML)
	}
	for _, a := range in.Attachments {
		out.Attachments = append(out.Attachments, mailpkg.OutgoingAttachment{Filename: a.Filename, ContentType: a.ContentType, Data: a.Data})
	}
	if f := in.ForwardFrom; f != nil {
		original, err := s.mail.Get(m.Address, f.Folder, f.UID)
		if err != nil {
			s.mailError(w, r, err)
			return nil, false
		}
		for _, meta := range original.Attachments {
			if f.Parts != nil && !slices.Contains(f.Parts, meta.Part) {
				continue
			}
			var part []int
			for _, seg := range strings.Split(meta.Part, ".") {
				n, _ := strconv.Atoi(seg)
				part = append(part, n)
			}
			a, err := s.mail.Attachment(m.Address, f.Folder, f.UID, part)
			if err != nil {
				s.mailError(w, r, err)
				return nil, false
			}
			out.Attachments = append(out.Attachments, mailpkg.OutgoingAttachment{Filename: a.Filename, ContentType: a.ContentType, Data: a.Body})
		}
	}
	if msg := validateAttachments(out.Attachments); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_attachments", msg)
		return nil, false
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
	var in outgoingRequest
	if !decode(w, r, 30<<20, &in) {
		return
	}
	out, ok := s.buildOutgoing(w, r, m, &in, true)
	if !ok {
		return
	}
	if in.SendAt != nil {
		s.scheduleMessage(w, r, m, &in, out)
		return
	}
	raw, messageID, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.mail.Send(out.From, out.Recipients(), raw); err != nil {
		mailSends.WithLabelValues("rejected").Inc()
		s.log.Warn("submission rejected", "mailbox", m.ID, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The mail server did not accept the message: "+err.Error())
		return
	}
	mailSends.WithLabelValues("accepted").Inc()
	if err := s.mail.SaveSent(m.Address, raw); err != nil {
		s.log.Error("saving to Sent failed", "mailbox", m.ID, "err", err)
	}
	if out.InReplyTo != "" {
		s.mail.MarkAnswered(m.Address, out.InReplyTo)
	}
	if in.DraftUID != 0 {
		if err := s.mail.DeleteDraft(m.Address, in.DraftUID); err != nil {
			s.log.Error("removing sent draft failed", "mailbox", m.ID, "err", err)
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"messageId": messageID})
}

func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	if !s.allow(r.Context(), "draft:"+m.ID, 120) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Drafts are being saved too often. Wait a moment.")
		return
	}
	var in outgoingRequest
	if !decode(w, r, 30<<20, &in) {
		return
	}
	out, ok := s.buildOutgoing(w, r, m, &in, false)
	if !ok {
		return
	}
	out.IncludeBcc = true
	raw, _, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	uid, folder, err := s.mail.SaveDraft(m.Address, raw, in.DraftUID)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	saved, err := s.mail.Get(m.Address, folder, uid)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid, "folder": folder, "attachments": saved.Attachments})
}

func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	if err := s.mail.DeleteDraft(m.Address, uid); err != nil {
		s.mailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const (
	maxAttachments     = 20
	maxAttachmentBytes = 20 << 20
)

func validateAttachments(list []mailpkg.OutgoingAttachment) string {
	if len(list) > maxAttachments {
		return "Attach 20 files or fewer per message."
	}
	total := 0
	for i := range list {
		a := &list[i]
		total += len(a.Data)
		a.Filename = strings.TrimSpace(path.Base(strings.ReplaceAll(a.Filename, "\\", "/")))
		if a.Filename == "" || a.Filename == "." || a.Filename == "/" || strings.ContainsAny(a.Filename, "\r\n\x00") || len(a.Filename) > 255 {
			return "One of the attachments has an invalid file name."
		}
		if mt, _, err := mime.ParseMediaType(a.ContentType); err != nil || !strings.Contains(mt, "/") {
			a.ContentType = "application/octet-stream"
		} else {
			a.ContentType = mt
		}
	}
	if total > maxAttachmentBytes {
		return "Attachments can total at most 20 MB per message."
	}
	return ""
}

func validMessageID(v string) bool {
	return len(v) > 2 && len(v) < 998 && strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") && !strings.ContainsAny(v[1:len(v)-1], "<> \r\n\t")
}

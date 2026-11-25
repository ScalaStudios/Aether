package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

type ownedMailbox struct {
	ID                   string
	OrgID                string
	DomainID             string
	Address              string
	DisplayName          string
	Kind, Access         string
	SendAs, SendOnBehalf bool
}

func (s *Server) accessibleMailbox(ctx context.Context, id, userID string) (*ownedMailbox, string, error) {
	m := &ownedMailbox{}
	err := s.db.QueryRow(ctx, `
		SELECT m.id, m.org_id, m.domain_id, m.address, m.display_name, m.kind, `+mailboxAccessLevel("$2")+`, `+mailboxSendAs("$2")+`, `+mailboxSendOnBehalf("$2")+` FROM mailboxes m JOIN domains d ON d.id = m.domain_id
		WHERE m.id = $1 AND `+mailboxAccess("$2")+` AND m.active AND 'webmail' = ANY(m.protocols) AND d.active AND d.verified_at IS NOT NULL`, id, userID).
		Scan(&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.DisplayName, &m.Kind, &m.Access, &m.SendAs, &m.SendOnBehalf)
	return m, m.Access, err
}

func (s *Server) ownedMailbox(w http.ResponseWriter, r *http.Request, p *principal) (*ownedMailbox, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, false
	}
	m, access, err := s.accessibleMailbox(r.Context(), id, p.User.ID)
	if err != nil {
		notFound(w)
		return nil, false
	}
	if access == "read" && r.Method != http.MethodGet {
		writeError(w, http.StatusForbidden, "read_only", "You have read-only access to this mailbox.")
		return nil, false
	}
	if p.apiKey == nil && m.Kind == "user" && m.Access == "owner" {
		s.touchWebLogin(r.Context(), m.ID)
	}
	return m, true
}

func (s *Server) sendingMailbox(w http.ResponseWriter, r *http.Request, p *principal) (*ownedMailbox, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, false
	}
	m, _, err := s.accessibleMailbox(r.Context(), id, p.User.ID)
	if err != nil {
		notFound(w)
		return nil, false
	}
	if !m.SendAs && !m.SendOnBehalf {
		writeError(w, http.StatusForbidden, "cannot_send", "You can read this mailbox but not send from it. Ask an administrator for Send as or Send on behalf.")
		return nil, false
	}
	if p.apiKey == nil && m.Kind == "user" && m.Access == "owner" {
		s.touchWebLogin(r.Context(), m.ID)
	}
	return m, true
}

func folderParam(r *http.Request) string {
	if f := r.URL.Query().Get("folder"); f != "" {
		return f
	}
	return "INBOX"
}

func parseDate(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.DateOnly, v)
}

func uidParam(w http.ResponseWriter, r *http.Request) (uint32, bool) {
	uid, err := strconv.ParseUint(r.PathValue("uid"), 10, 32)
	if err != nil || uid == 0 {
		notFound(w)
		return 0, false
	}
	return uint32(uid), true
}

var mailErrors = []struct {
	err           error
	status        int
	code, message string
}{
	{mailpkg.ErrNotFound, http.StatusNotFound, "not_found", "The folder or message no longer exists. It may have been moved or deleted from another client."},
	{mailpkg.ErrTooLarge, http.StatusUnprocessableEntity, "message_too_large", "This message is too large to open here."},
	{mailpkg.ErrInvalidCursor, http.StatusUnprocessableEntity, "invalid_cursor", "The page cursor is not valid. Load the list again from the first page."},
	{mailpkg.ErrFolderExists, http.StatusConflict, "folder_exists", "A folder with this name already exists."},
	{mailpkg.ErrProtectedFolder, http.StatusUnprocessableEntity, "protected_folder", "Inbox, Drafts, Sent, Archive, Junk, Trash, and Snoozed cannot be renamed, moved, or deleted."},
	{mailpkg.ErrFolderHasChildren, http.StatusConflict, "folder_has_subfolders", "This folder has subfolders. Move or delete them first."},
	{mailpkg.ErrFolderNotEmpty, http.StatusConflict, "folder_not_empty", "This folder still has messages. Choose a folder to move them to first."},
	{mailpkg.ErrOverQuota, http.StatusInsufficientStorage, "over_quota", "The mailbox is full, so nothing was restored. Delete mail or ask an administrator for more space, then try again."},
}

func (s *Server) mailError(w http.ResponseWriter, r *http.Request, err error) {
	for _, e := range mailErrors {
		if errors.Is(err, e.err) {
			writeError(w, e.status, e.code, e.message)
			return
		}
	}
	mailBackendErrs.Inc()
	s.log.Error("mail backend request failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusBadGateway, "mail_unavailable", "The mail server did not respond as expected. Try again in a moment.")
}

func (s *Server) listFolders(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.managedMailbox(w, r, p)
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
	o := mailpkg.ListOptions{
		Query: q.Get("q"), Sort: cmp.Or(q.Get("sort"), "date"), Desc: q.Get("order") != "asc", Threads: q.Get("threads") == "true",
		Unread: q.Get("unread") == "true", Flagged: q.Get("flagged") == "true", HasAttachments: q.Get("hasAttachments") == "true",
		From: strings.TrimSpace(q.Get("from")), To: strings.TrimSpace(q.Get("to")), Cursor: q.Get("cursor"), Limit: limit,
	}
	if _, ok := mailpkg.SortKeys[o.Sort]; !ok || !slices.Contains([]string{"", "asc", "desc"}, q.Get("order")) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_sort", "Sort by date, sent, from, to, subject, or size, in asc or desc order.")
		return
	}
	var sinceErr, untilErr error
	o.Since, sinceErr = parseDate(q.Get("since"))
	o.Until, untilErr = parseDate(q.Get("until"))
	if sinceErr != nil || untilErr != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_date", "Write since and until as dates like 2026-10-31.")
		return
	}
	folder := folderParam(r)
	all := false
	if !s.resolveList(w, r, m, &o, &folder, &all) {
		return
	}
	var page *mailpkg.Page
	if all {
		folders, ferr := s.mail.Folders(m.Address)
		if ferr != nil {
			s.mailError(w, r, ferr)
			return
		}
		names := make([]string, 0, len(folders))
		for _, f := range folders {
			names = append(names, f.Name)
		}
		page, err = s.mail.ListAcross(m.Address, names, o)
	} else {
		page, err = s.mail.List(m.Address, folder, o)
	}
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	s.recordAccess(r, p, m, "folder.open", folderParam(r), nil, nil)
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
	s.recordAccess(r, p, m, "message.read", folderParam(r), []accessItem{{UID: uid, Subject: msg.Subject, MessageID: msg.MessageID}}, nil)
	s.writeMessage(w, r, p, m, msg)
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
		Seen         *bool    `json:"seen"`
		Flagged      *bool    `json:"flagged"`
		AddLabels    []string `json:"addLabels"`
		RemoveLabels []string `json:"removeLabels"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if in.Seen == nil && in.Flagged == nil && len(in.AddLabels) == 0 && len(in.RemoveLabels) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message", "Choose what to change on the message.")
		return
	}
	if in.Seen != nil || in.Flagged != nil {
		if err := s.mail.SetFlags(m.Address, folderParam(r), uid, in.Seen, in.Flagged); err != nil {
			s.mailError(w, r, err)
			return
		}
	}
	if len(in.AddLabels) > 0 || len(in.RemoveLabels) > 0 {
		if !s.applyLabels(w, r, m, folderParam(r), []uint32{uid}, in.AddLabels, in.RemoveLabels) {
			return
		}
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
	commit := s.accessAudit(r, p, m, "message.delete", folderParam(r), uid)
	if err := s.mail.Delete(m.Address, folderParam(r), uid); err != nil {
		s.mailError(w, r, err)
		return
	}
	commit(nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) moveMessages(w http.ResponseWriter, r *http.Request, p *principal) {
	s.transferMessages(w, r, p, false)
}

func (s *Server) copyMessages(w http.ResponseWriter, r *http.Request, p *principal) {
	s.transferMessages(w, r, p, true)
}

func (s *Server) transferMessages(w http.ResponseWriter, r *http.Request, p *principal, keep bool) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var in struct {
		To        string   `json:"to"`
		MailboxID string   `json:"mailboxId"`
		UIDs      []uint32 `json:"uids"`
	}
	if !decode(w, r, 16<<10, &in) {
		return
	}
	uids := in.UIDs
	if r.PathValue("uid") != "" {
		uid, ok := uidParam(w, r)
		if !ok {
			return
		}
		uids = []uint32{uid}
	}
	switch {
	case in.To == "":
		writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "Choose a destination folder.")
		return
	case len(uids) == 0 || len(uids) > 500 || slices.Contains(uids, 0):
		writeError(w, http.StatusUnprocessableEntity, "invalid_messages", "Choose between 1 and 500 messages.")
		return
	}
	folder, targetID := folderParam(r), strings.ToLower(in.MailboxID)
	accessAction := "message.move"
	if keep {
		accessAction = "message.copy"
	}
	if targetID == "" || targetID == m.ID {
		commit := s.accessAudit(r, p, m, accessAction, folder, uids...)
		if err := s.mail.Move(m.Address, folder, uids, in.To, keep); err != nil {
			s.mailError(w, r, err)
			return
		}
		commit(map[string]any{"to": in.To})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !uuidPattern.MatchString(targetID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_mailbox", "Choose a mailbox you have full access to.")
		return
	}
	target, access, err := s.accessibleMailbox(r.Context(), targetID, p.User.ID)
	switch {
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, "invalid_mailbox", "Choose a mailbox you have full access to.")
		return
	case access == "read":
		writeError(w, http.StatusForbidden, "read_only", "You have read-only access to the destination mailbox.")
		return
	}
	commit := s.accessAudit(r, p, m, accessAction, folder, uids...)
	n, err := s.mail.Transfer(m.Address, folder, uids, target.Address, in.To, keep)
	if n > 0 {
		commit(map[string]any{"to": in.To, "toMailbox": target.Address, "transferred": n})
		action := "mailbox.messages.move"
		if keep {
			action = "mailbox.messages.copy"
		}
		s.audit(r.Context(), r, p, &m.OrgID, action, "mailbox", m.ID, map[string]any{
			"from": m.Address, "fromFolder": folder, "to": target.Address, "toMailboxId": target.ID, "toFolder": in.To, "messages": n,
		})
	}
	if err != nil {
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
	s.recordAccess(r, p, m, "message.read", folderParam(r), []accessItem{{UID: uid}}, map[string]any{"attachment": a.Filename})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mailpkg.ContentDisposition(a.Filename))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Length", strconv.Itoa(len(a.Body)))
	w.Write(a.Body)
}

func (s *Server) messageSource(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	uid, ok := uidParam(w, r)
	if !ok {
		return
	}
	raw, err := s.mail.Source(m.Address, folderParam(r), uid)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename=\"message.eml\"")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Write(raw)
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
	FromAddress string            `json:"fromAddress"`
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
	DraftUID  uint32        `json:"draftUid"`
	FileLinks []fileLinkRef `json:"fileLinks"`
	SendAt    *time.Time    `json:"sendAt"`
	Sealed    *struct {
		ContentType string `json:"contentType"`
		Body        []byte `json:"body"`
	} `json:"sealed"`
	Autocrypt string `json:"autocrypt"`
}

func (s *Server) buildOutgoing(w http.ResponseWriter, r *http.Request, m *ownedMailbox, from mailpkg.Address, in *outgoingRequest, requireRecipients bool) (*mailpkg.Outgoing, bool) {
	out := &mailpkg.Outgoing{From: from, Subject: in.Subject, Text: in.Text, InReplyTo: in.InReplyTo}
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
	if in.Autocrypt != "" {
		header, err := mailpkg.NormalizeAutocrypt(in.Autocrypt, m.Address)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_autocrypt", "The OpenPGP key for this sender could not be attached.")
			return nil, false
		}
		out.Autocrypt = header
	}
	if in.Sealed != nil {
		if in.Text != "" || in.HTML != "" || len(in.Attachments) > 0 || in.ForwardFrom != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_encryption", "An encrypted or signed message cannot also include an unprotected copy.")
			return nil, false
		}
		sealed, err := mailpkg.ClassifySealed(in.Sealed.ContentType, in.Sealed.Body)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_encryption", "This message is not valid S/MIME or OpenPGP.")
			return nil, false
		}
		out.Sealed = sealed
		return out, true
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
	if msg := normalizeAttachments(out.Attachments); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_attachments", msg)
		return nil, false
	}
	return out, true
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.sendingMailbox(w, r, p)
	if !ok {
		return
	}
	if !s.allow(r.Context(), "send:"+m.ID, 30) {
		mailRateLimited.Inc()
		s.recordAlertEvent(r.Context(), "rate_limited_senders", m.Address)
		writeError(w, http.StatusTooManyRequests, "rate_limited", "This mailbox is sending too quickly. Wait a minute before sending again.")
		return
	}
	var in outgoingRequest
	if !decode(w, r, s.outgoingBodyLimit(), &in) {
		return
	}
	from, sentAddress, ok := s.resolveSender(w, r, p, m, in.FromAddress)
	if !ok {
		return
	}
	out, ok := s.buildOutgoing(w, r, m, from, &in, true)
	if !ok {
		return
	}
	method, encrypted := "", false
	if out.Sealed != nil {
		method, encrypted = out.Sealed.Protocol, out.Sealed.Encrypted
	}
	if !s.allowEncryption(w, r, m.OrgID, method, encrypted, out.Autocrypt != "", out.Recipients()) {
		return
	}
	if !m.SendAs {
		out.Sender = &mailpkg.Address{Name: p.User.DisplayName, Address: p.User.Email}
	}
	var sendAt *time.Time
	undo := false
	if in.SendAt != nil {
		at, ok := s.readyToSchedule(w, r, m, *in.SendAt, false)
		if !ok {
			return
		}
		sendAt = &at
		out.Date = at
	} else {
		delay, err := s.undoDelay(r.Context(), p.User.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if delay > 0 {
			at, ok := s.readyToSchedule(w, r, m, time.Now().Add(delay), true)
			if !ok {
				return
			}
			sendAt = &at
			undo = true
		}
	}
	staged, ok := s.stageFileLinks(w, r, p, m, in.FileLinks, out, sendAt, true)
	if !ok {
		return
	}
	raw, messageID, ok := s.sealMessage(w, r, staged, out)
	if !ok {
		return
	}
	if sendAt != nil {
		s.insertScheduled(w, r, p, m, out, raw, *sendAt, in.DraftUID, staged.created, undo)
		return
	}
	if err := s.mail.Send(m.Address, out.From.Address, out.Recipients(), raw); err != nil {
		s.deleteFileLinks(r.Context(), m.ID, staged.created)
		mailSends.WithLabelValues("rejected").Inc()
		s.recordAlertEvent(r.Context(), "delivery_failures", m.Address)
		s.log.Warn("submission rejected", "mailbox", m.ID, "err", err)
		writeError(w, http.StatusBadGateway, "send_failed", "The mail server did not accept the message: "+err.Error())
		return
	}
	mailSends.WithLabelValues("accepted").Inc()
	s.recordAccess(r, p, m, sendAction(m), "", []accessItem{{Subject: out.Subject, MessageID: messageID}}, map[string]any{"recipients": len(out.Recipients())})
	if err := s.mail.SaveSent(sentAddress, raw); err != nil {
		s.log.Error("saving to Sent failed", "mailbox", m.ID, "err", err)
	}
	if m.Access == "read" {
		writeJSON(w, http.StatusAccepted, map[string]string{"messageId": messageID})
		return
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
	if !decode(w, r, s.outgoingBodyLimit(), &in) {
		return
	}
	from, _, ok := s.resolveSender(w, r, p, m, in.FromAddress)
	if !ok {
		return
	}
	out, ok := s.buildOutgoing(w, r, m, from, &in, false)
	if !ok {
		return
	}
	staged, ok := s.stageFileLinks(w, r, p, m, in.FileLinks, out, nil, false)
	if !ok {
		return
	}
	out.IncludeBcc = true
	raw, _, ok := s.sealMessage(w, r, staged, out)
	if !ok {
		return
	}
	uid, folder, err := s.mail.SaveDraft(m.Address, raw, in.DraftUID)
	if err != nil {
		s.deleteFileLinks(r.Context(), m.ID, staged.created)
		s.mailError(w, r, err)
		return
	}
	saved, err := s.mail.Get(m.Address, folder, uid)
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid, "folder": folder, "attachments": saved.Attachments, "fileLinks": nonNil(staged.summaries)})
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
	s.recordAccess(r, p, m, "message.delete", "Drafts", []accessItem{{UID: uid}}, map[string]any{"draft": true})
	w.WriteHeader(http.StatusNoContent)
}

func validMessageID(v string) bool {
	return len(v) > 2 && len(v) < 998 && strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") && !strings.ContainsAny(v[1:len(v)-1], "<> \r\n\t")
}

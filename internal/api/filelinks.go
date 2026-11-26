package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/auth"
	mailpkg "github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/secrets"
)

var (
	errLinkTooLarge  = errors.New("file link too large")
	errLinkEmpty     = errors.New("file link empty")
	fileTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

type fileLinkRef struct {
	ID            string     `json:"id"`
	Password      *string    `json:"password"`
	ClearPassword bool       `json:"clearPassword"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	ClearExpiry   bool       `json:"clearExpiry"`
	passwordHash  string
}

type fileLinkSummary struct {
	ID                string     `json:"id"`
	Filename          string     `json:"filename"`
	ContentType       string     `json:"contentType"`
	SizeBytes         int64      `json:"sizeBytes"`
	ExpiresAt         *time.Time `json:"expiresAt"`
	PasswordProtected bool       `json:"passwordProtected"`
}

type fileLinkRecord struct {
	fileLinkSummary
	DownloadCount  int64     `json:"downloadCount"`
	CreatedAt      time.Time `json:"createdAt"`
	MailboxAddress string    `json:"mailboxAddress"`
	URL            string    `json:"url"`
	Committed      bool      `json:"committed"`
	Expired        bool      `json:"expired"`
}

type fileLinkPublic struct {
	Filename         string     `json:"filename"`
	ContentType      string     `json:"contentType"`
	SizeBytes        int64      `json:"sizeBytes"`
	ExpiresAt        *time.Time `json:"expiresAt"`
	PasswordRequired bool       `json:"passwordRequired"`
}

type linkNotice struct {
	ID, Filename, ContentType, URL string
	Size                           int64
	Expires                        *time.Time
	Password                       bool
}

type stagedLinks struct {
	tx        pgx.Tx
	ids       []string
	created   []string
	summaries []fileLinkSummary
	limits    transferLimits
}

func (st *stagedLinks) rollback(ctx context.Context) {
	if st == nil || st.tx == nil {
		return
	}
	st.tx.Rollback(ctx)
	st.tx = nil
}

func (st *stagedLinks) commit(ctx context.Context) error {
	if st == nil || st.tx == nil {
		return nil
	}
	err := st.tx.Commit(ctx)
	st.tx = nil
	return err
}

func (s *Server) stageFileLinks(w http.ResponseWriter, r *http.Request, p *principal, m *ownedMailbox, refs []fileLinkRef, out *mailpkg.Outgoing, sendAt *time.Time, sending bool) (*stagedLinks, bool) {
	limits, err := s.transferLimits(r.Context(), m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	original := append([]mailpkg.OutgoingAttachment(nil), out.Attachments...)
	if len(refs) == 0 {
		inline, linked, msg := partitionAttachments(original, limits)
		if msg != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_attachments", msg)
			return nil, false
		}
		if len(linked) == 0 {
			if msg := validateAttachments(inline, limits.AttachmentBytes); msg != "" {
				writeError(w, http.StatusUnprocessableEntity, "invalid_attachments", msg)
				return nil, false
			}
			out.Attachments = inline
			out.FileLinkIDs = nil
			return &stagedLinks{limits: limits}, true
		}
	}
	if len(refs) > maxFileLinks {
		writeError(w, http.StatusUnprocessableEntity, "too_many_links", fmt.Sprintf("Send %d download links or fewer per message.", maxFileLinks))
		return nil, false
	}
	for i, ref := range refs {
		if !ref.ClearPassword && ref.Password != nil && *ref.Password != "" && validateLinkPassword(*ref.Password) == "" {
			refs[i].passwordHash = auth.HashPassword(*ref.Password)
		}
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	fail := func(status int, code, msg string, cause error) (*stagedLinks, bool) {
		tx.Rollback(r.Context())
		if cause != nil {
			s.internalError(w, r, cause)
			return nil, false
		}
		writeError(w, status, code, msg)
		return nil, false
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO organization_transfer_limits (org_id) VALUES ($1) ON CONFLICT DO NOTHING`, m.OrgID); err != nil {
		return fail(0, "", "", err)
	}
	var stored transferLimits
	err = tx.QueryRow(r.Context(), `
		SELECT message_bytes, attachment_bytes, link_threshold_bytes, link_file_bytes, storage_bytes
		FROM organization_transfer_limits WHERE org_id = $1 FOR UPDATE`, m.OrgID).
		Scan(&stored.MessageBytes, &stored.AttachmentBytes, &stored.LinkThresholdBytes, &stored.LinkFileBytes, &stored.StorageBytes)
	if err != nil {
		return fail(0, "", "", err)
	}
	limits = stored.effective(s.platformCeilings())
	inline, linked, msg := partitionAttachments(original, limits)
	if msg != "" {
		return fail(http.StatusUnprocessableEntity, "invalid_attachments", msg, nil)
	}
	if len(refs)+len(linked) > maxFileLinks {
		return fail(http.StatusUnprocessableEntity, "too_many_links", fmt.Sprintf("Send %d download links or fewer per message.", maxFileLinks), nil)
	}
	if msg := validateAttachments(inline, limits.AttachmentBytes); msg != "" {
		return fail(http.StatusUnprocessableEntity, "invalid_attachments", msg, nil)
	}
	used, err := s.linkUsage(r.Context(), tx, m.OrgID)
	if err != nil {
		return fail(0, "", "", err)
	}
	notBefore := time.Now()
	if sendAt != nil {
		notBefore = sendAt.UTC()
	}
	st := &stagedLinks{tx: tx, limits: limits}
	var notices []linkNotice
	seen := map[string]struct{}{}
	for _, ref := range refs {
		id := strings.ToLower(ref.ID)
		if !uuidPattern.MatchString(id) {
			return fail(http.StatusUnprocessableEntity, "invalid_file_link", "One of the download links is not valid. Attach the file again.", nil)
		}
		if _, ok := seen[id]; ok {
			return fail(http.StatusUnprocessableEntity, "invalid_file_link", "The same download link is listed more than once.", nil)
		}
		seen[id] = struct{}{}
		var token, filename, contentType string
		var size int64
		var hash *string
		var expires *time.Time
		err = tx.QueryRow(r.Context(), `
			SELECT token, filename, content_type, size_bytes, password_hash, expires_at
			FROM file_links WHERE id = $1 AND mailbox_id = $2 AND org_id = $3 FOR UPDATE`, id, m.ID, m.OrgID).
			Scan(&token, &filename, &contentType, &size, &hash, &expires)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(http.StatusUnprocessableEntity, "invalid_file_link", "One of the download links is not available. Attach the file again.", nil)
		}
		if err != nil {
			return fail(0, "", "", err)
		}
		if expires != nil && !expires.After(time.Now()) {
			return fail(http.StatusUnprocessableEntity, "file_link_expired", filename+" has expired. Attach the file again.", nil)
		}
		hash, expires, msg = resolveLinkAccess(ref, hash, expires, notBefore)
		if msg != "" {
			return fail(http.StatusUnprocessableEntity, "invalid_file_link", msg, nil)
		}
		if _, err = tx.Exec(r.Context(), `UPDATE file_links SET password_hash = $2, expires_at = $3 WHERE id = $1`, id, hash, expires); err != nil {
			return fail(0, "", "", err)
		}
		notices = append(notices, linkNotice{ID: id, Filename: filename, ContentType: contentType, URL: s.fileLinkURL(token), Size: size, Expires: expires, Password: hash != nil})
		st.ids = append(st.ids, id)
	}
	var pending int64
	for _, a := range linked {
		size := int64(len(a.Data))
		if size > limits.StorageBytes || used+pending+size > limits.StorageBytes {
			return fail(http.StatusUnprocessableEntity, "storage_full", storageFullMessage(a.Filename, size, used+pending, limits.StorageBytes), nil)
		}
		oid, n, err := writeLargeObject(r.Context(), tx, bytes.NewReader(a.Data), limits.LinkFileBytes)
		if errors.Is(err, errLinkTooLarge) || errors.Is(err, errLinkEmpty) {
			return fail(http.StatusUnprocessableEntity, "file_too_large", fmt.Sprintf("%s is %s, over the %s limit for a file.", displayFilename(a.Filename), formatByteSize(size), formatByteSize(limits.LinkFileBytes)), nil)
		}
		if err != nil {
			return fail(0, "", "", err)
		}
		token, err := newFileToken()
		if err != nil {
			return fail(0, "", "", err)
		}
		id, err := insertFileLink(r.Context(), tx, m, p.User.ID, a.Filename, a.ContentType, token, n, oid)
		if err != nil {
			return fail(0, "", "", err)
		}
		pending += n
		notices = append(notices, linkNotice{ID: id, Filename: a.Filename, ContentType: a.ContentType, URL: s.fileLinkURL(token), Size: n})
		st.ids = append(st.ids, id)
		st.created = append(st.created, id)
	}
	if sending && len(st.ids) > 0 {
		if _, err = tx.Exec(r.Context(), `UPDATE file_links SET committed_at = coalesce(committed_at, now()) WHERE mailbox_id = $1 AND id::text = ANY($2)`, m.ID, st.ids); err != nil {
			return fail(0, "", "", err)
		}
	}
	out.Attachments = inline
	if sending {
		applyLinkSection(out, notices)
	} else {
		assignLinkIDs(out, notices)
	}
	for _, n := range notices {
		st.summaries = append(st.summaries, fileLinkSummary{
			ID: n.ID, Filename: n.Filename, ContentType: n.ContentType, SizeBytes: n.Size, ExpiresAt: n.Expires, PasswordProtected: n.Password,
		})
	}
	return st, true
}

func (s *Server) sealMessage(w http.ResponseWriter, r *http.Request, st *stagedLinks, out *mailpkg.Outgoing) ([]byte, string, bool) {
	raw, messageID, err := out.Build(s.cfg.MailHostname)
	if err != nil {
		st.rollback(r.Context())
		s.internalError(w, r, err)
		return nil, "", false
	}
	if msg := messageSizeError(raw, st.limits.MessageBytes); msg != "" {
		st.rollback(r.Context())
		writeError(w, http.StatusUnprocessableEntity, "message_too_large", msg)
		return nil, "", false
	}
	if err := st.commit(r.Context()); err != nil {
		s.internalError(w, r, err)
		return nil, "", false
	}
	return raw, messageID, true
}

func (s *Server) deleteFileLinks(ctx context.Context, mailboxID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM file_links WHERE mailbox_id = $1 AND id::text = ANY($2)`, mailboxID, ids); err != nil {
		s.log.Error("removing file links failed", "mailbox", mailboxID, "err", err)
	}
}

func storageFullMessage(filename string, size, used, storage int64) string {
	if size > storage {
		return fmt.Sprintf("%s is %s, over the %s storage limit for download links.", displayFilename(filename), formatByteSize(size), formatByteSize(storage))
	}
	return fmt.Sprintf("Download storage is full: %s of %s is in use. Remove older links, or ask an administrator to raise the limit.", formatByteSize(used), formatByteSize(storage))
}

func resolveLinkAccess(ref fileLinkRef, currentHash *string, currentExpiry *time.Time, notBefore time.Time) (*string, *time.Time, string) {
	if ref.ClearPassword && ref.Password != nil && *ref.Password != "" {
		return nil, nil, "Enter a new download password or remove the current one."
	}
	if ref.ClearExpiry && ref.ExpiresAt != nil {
		return nil, nil, "Choose an expiry or clear it."
	}
	hash := currentHash
	switch {
	case ref.ClearPassword:
		hash = nil
	case ref.Password != nil && *ref.Password != "":
		if msg := validateLinkPassword(*ref.Password); msg != "" {
			return nil, nil, msg
		}
		hash = &ref.passwordHash
	}
	expires := currentExpiry
	switch {
	case ref.ClearExpiry:
		expires = nil
	case ref.ExpiresAt != nil:
		expires = ref.ExpiresAt
	}
	if expires != nil {
		if msg := validateLinkExpiry(expires.UTC(), notBefore); msg != "" {
			return nil, nil, msg
		}
		utc := expires.UTC()
		expires = &utc
	}
	return hash, expires, ""
}

func writeLargeObject(ctx context.Context, tx pgx.Tx, r io.Reader, max int64) (uint32, int64, error) {
	los := tx.LargeObjects()
	oid, err := los.Create(ctx, 0)
	if err != nil {
		return 0, 0, err
	}
	obj, err := los.Open(ctx, oid, pgx.LargeObjectModeWrite)
	if err != nil {
		return 0, 0, err
	}
	defer obj.Close()
	n, err := io.CopyBuffer(obj, io.LimitReader(r, max+1), make([]byte, 256<<10))
	if err != nil {
		return 0, n, err
	}
	if n > max {
		return 0, n, errLinkTooLarge
	}
	if n == 0 {
		return 0, 0, errLinkEmpty
	}
	return oid, n, nil
}

func newFileToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func insertFileLink(ctx context.Context, tx pgx.Tx, m *ownedMailbox, userID, filename, contentType, token string, size int64, oid uint32) (string, error) {
	var createdBy *string
	if userID != "" {
		createdBy = &userID
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO file_links (org_id, mailbox_id, created_by, token, filename, content_type, size_bytes, object_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		m.OrgID, m.ID, createdBy, token, filename, contentType, size, int64(oid)).Scan(&id)
	return id, err
}

func (s *Server) fileLinkURL(token string) string {
	return s.cfg.PublicURL.JoinPath("files", token).String()
}

func assignLinkIDs(out *mailpkg.Outgoing, notices []linkNotice) {
	out.FileLinkIDs = nil
	for _, n := range notices {
		out.FileLinkIDs = append(out.FileLinkIDs, n.ID)
	}
}

func applyLinkSection(out *mailpkg.Outgoing, notices []linkNotice) {
	assignLinkIDs(out, notices)
	if len(notices) == 0 {
		return
	}
	original := out.Text
	extra := linkSectionText(notices)
	if strings.TrimSpace(original) == "" {
		out.Text = extra
	} else {
		out.Text = strings.TrimRight(original, "\n") + "\n\n" + extra
	}
	block := linkSectionHTML(notices)
	if strings.TrimSpace(out.HTML) == "" {
		out.HTML = plainTextHTML(original) + block
	} else {
		out.HTML += block
	}
}

func linkSectionText(notices []linkNotice) string {
	var b strings.Builder
	b.WriteString("Large files\n")
	for _, n := range notices {
		fmt.Fprintf(&b, "\n%s (%s)\n%s\n", n.Filename, formatByteSize(n.Size), n.URL)
		if n.Expires != nil {
			fmt.Fprintf(&b, "Expires %s.\n", n.Expires.UTC().Format("2 Jan 2006 15:04 MST"))
		}
		if n.Password {
			b.WriteString("A password is required. It is not included in this message.\n")
		}
	}
	b.WriteString("\nThese files are not attached to the message.\n")
	return b.String()
}

func linkSectionHTML(notices []linkNotice) string {
	var b strings.Builder
	b.WriteString(`<div><p><strong>Large files</strong></p><ul>`)
	for _, n := range notices {
		fmt.Fprintf(&b, `<li><a href="%s" target="_blank" rel="noopener noreferrer">%s</a> (%s)`, html.EscapeString(n.URL), html.EscapeString(n.Filename), html.EscapeString(formatByteSize(n.Size)))
		if n.Expires != nil {
			fmt.Fprintf(&b, " — expires %s", html.EscapeString(n.Expires.UTC().Format("2 Jan 2006 15:04 MST")))
		}
		switch {
		case n.Password:
			b.WriteString(". A password is required and is not included in this message.")
		default:
			b.WriteString(".")
		}
		b.WriteString("</li>")
	}
	b.WriteString(`</ul><p>These files are not attached to the message.</p></div>`)
	return b.String()
}

func plainTextHTML(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var b strings.Builder
	for _, block := range strings.Split(text, "\n\n") {
		b.WriteString("<p>")
		b.WriteString(strings.ReplaceAll(html.EscapeString(block), "\n", "<br>"))
		b.WriteString("</p>")
	}
	return b.String()
}

func (s *Server) uploadFileLink(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.sendingMailbox(w, r, p)
	if !ok {
		return
	}
	if !s.allow(r.Context(), "upload:"+m.ID, 30) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many files are being uploaded from this mailbox. Wait a minute and try again.")
		return
	}
	file := mailpkg.OutgoingAttachment{Filename: r.URL.Query().Get("filename"), ContentType: r.Header.Get("Content-Type")}
	if msg := normalizeAttachment(&file); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_attachments", "Use a file name without a path or control characters, up to 255 characters.")
		return
	}
	limits, err := s.transferLimits(r.Context(), m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if r.ContentLength > limits.LinkFileBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", fmt.Sprintf("%s is over the %s limit for a download link.", displayFilename(file.Filename), formatByteSize(limits.LinkFileBytes)))
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `INSERT INTO organization_transfer_limits (org_id) VALUES ($1) ON CONFLICT DO NOTHING`, m.OrgID); err != nil {
		s.internalError(w, r, err)
		return
	}
	var stored transferLimits
	err = tx.QueryRow(r.Context(), `
		SELECT message_bytes, attachment_bytes, link_threshold_bytes, link_file_bytes, storage_bytes
		FROM organization_transfer_limits WHERE org_id = $1 FOR UPDATE`, m.OrgID).
		Scan(&stored.MessageBytes, &stored.AttachmentBytes, &stored.LinkThresholdBytes, &stored.LinkFileBytes, &stored.StorageBytes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	limits = stored.effective(s.platformCeilings())
	used, err := s.linkUsage(r.Context(), tx, m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if used >= limits.StorageBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "storage_full", storageFullMessage(file.Filename, 1, used, limits.StorageBytes))
		return
	}
	room := limits.LinkFileBytes
	if left := limits.StorageBytes - used; left < room {
		room = left
	}
	oid, size, err := writeLargeObject(r.Context(), tx, r.Body, room)
	if errors.Is(err, errLinkEmpty) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_attachments", "The file is empty.")
		return
	}
	if errors.Is(err, errLinkTooLarge) {
		code, message := "file_too_large", fmt.Sprintf("%s is over the %s limit for a download link.", displayFilename(file.Filename), formatByteSize(limits.LinkFileBytes))
		if room < limits.LinkFileBytes {
			code, message = "storage_full", storageFullMessage(file.Filename, room+1, used, limits.StorageBytes)
		}
		writeError(w, http.StatusRequestEntityTooLarge, code, message)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	token, err := newFileToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	id, err := insertFileLink(r.Context(), tx, m, p.User.ID, file.Filename, file.ContentType, token, size, oid)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "file_link.create", "file_link", id, map[string]any{"filename": file.Filename, "bytes": size, "mailboxId": m.ID})
	writeJSON(w, http.StatusCreated, fileLinkSummary{ID: id, Filename: file.Filename, ContentType: file.ContentType, SizeBytes: size})
}

func (s *Server) deleteMailboxFileLink(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.sendingMailbox(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "linkId")
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM file_links WHERE id = $1 AND mailbox_id = $2 AND org_id = $3`, id, m.ID, m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "file_link.delete", "file_link", id, map[string]any{"mailboxId": m.ID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listOrgFileLinks(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT f.id, f.filename, f.content_type, f.size_bytes, f.expires_at, f.password_hash IS NOT NULL,
		       f.download_count, f.created_at, f.committed_at IS NOT NULL, f.expires_at IS NOT NULL AND f.expires_at <= now(),
		       f.token, m.address
		FROM file_links f JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE f.org_id = $1 ORDER BY f.created_at DESC LIMIT 200`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()
	var out []fileLinkRecord
	for rows.Next() {
		var item fileLinkRecord
		var token string
		if err := rows.Scan(&item.ID, &item.Filename, &item.ContentType, &item.SizeBytes, &item.ExpiresAt, &item.PasswordProtected, &item.DownloadCount, &item.CreatedAt, &item.Committed, &item.Expired, &token, &item.MailboxAddress); err != nil {
			s.internalError(w, r, err)
			return
		}
		item.URL = s.fileLinkURL(token)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) deleteOrgFileLink(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "linkId")
	if !ok {
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM file_links WHERE id = $1 AND org_id = $2`, id, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		notFound(w)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "file_link.delete", "file_link", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

type storedFileLink struct {
	filename, contentType string
	size, objectID        int64
	password              *string
	expires               *time.Time
}

func (s *Server) fileByToken(ctx context.Context, token string) (storedFileLink, error) {
	var row storedFileLink
	err := s.db.QueryRow(ctx, `
		SELECT filename, content_type, size_bytes, object_id, password_hash, expires_at
		FROM file_links WHERE token = $1`, token).Scan(&row.filename, &row.contentType, &row.size, &row.objectID, &row.password, &row.expires)
	return row, err
}

func (s *Server) rejectFileToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	token := r.PathValue("token")
	if !fileTokenPattern.MatchString(token) {
		notFound(w)
		return "", false
	}
	if !s.allow(r.Context(), "filelink:"+s.clientIP(r), 300) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many download attempts from this network. Wait a minute and try again.")
		return "", false
	}
	return token, true
}

func fileExpired(row storedFileLink) bool {
	return row.expires != nil && !row.expires.After(time.Now())
}

func (s *Server) getFileLinkMeta(w http.ResponseWriter, r *http.Request) {
	token, ok := s.rejectFileToken(w, r)
	if !ok {
		return
	}
	row, err := s.fileByToken(r.Context(), token)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if fileExpired(row) {
		writeError(w, http.StatusGone, "file_link_expired", "This download link has expired.")
		return
	}
	writeJSON(w, http.StatusOK, fileLinkPublic{
		Filename: row.filename, ContentType: row.contentType, SizeBytes: row.size, ExpiresAt: row.expires, PasswordRequired: row.password != nil,
	})
}

func (s *Server) downloadFileLink(w http.ResponseWriter, r *http.Request) {
	token, ok := s.rejectFileToken(w, r)
	if !ok {
		return
	}
	var password string
	if r.Method == http.MethodPost {
		var in struct {
			Password string `json:"password"`
		}
		if !decode(w, r, 4<<10, &in) {
			return
		}
		password = in.Password
	}
	row, err := s.fileByToken(r.Context(), token)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if fileExpired(row) {
		writeError(w, http.StatusGone, "file_link_expired", "This download link has expired.")
		return
	}
	if row.password != nil {
		if password == "" {
			writeError(w, http.StatusUnauthorized, "password_required", "Enter the password to download this file.")
			return
		}
		if !s.allow(r.Context(), "filepw:"+secrets.SHA256Hex(token), 8) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many password attempts. Wait a minute and try again.")
			return
		}
		match, err := auth.VerifyPassword(r.Context(), *row.password, password)
		if errors.Is(err, auth.ErrPasswordBusy) {
			passwordBusy(w)
			return
		}
		if err != nil || !match {
			writeError(w, http.StatusUnauthorized, "invalid_password", "That password is not correct.")
			return
		}
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE file_links SET download_count = download_count + 1, last_download_at = now() WHERE token = $1`, token); err != nil {
		s.log.Error("counting file download failed", "err", err)
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": row.filename})
	if disposition == "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", row.contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(row.size, 10))
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	if err := s.streamLargeObject(r.Context(), w, row.objectID, row.size); err != nil {
		s.log.Warn("file download interrupted", "err", err)
	}
}

func (s *Server) streamLargeObject(ctx context.Context, w io.Writer, objectID, size int64) error {
	var off int64
	for off < size {
		n := int32(fileChunkBytes)
		if remain := size - off; remain < int64(n) {
			n = int32(remain)
		}
		var chunk []byte
		if err := s.db.QueryRow(ctx, `SELECT lo_get($1::oid, $2, $3)`, objectID, off, n).Scan(&chunk); err != nil {
			return err
		}
		if len(chunk) == 0 {
			return io.ErrUnexpectedEOF
		}
		if _, err := w.Write(chunk); err != nil {
			return err
		}
		off += int64(len(chunk))
	}
	return nil
}

func (s *Server) messageFileLinks(ctx context.Context, mailboxID string, ids []string) ([]fileLinkSummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT id::text, filename, content_type, size_bytes, expires_at, password_hash IS NOT NULL
		FROM file_links WHERE mailbox_id = $1 AND id::text = ANY($2)`, mailboxID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]fileLinkSummary{}
	for rows.Next() {
		var item fileLinkSummary
		if err := rows.Scan(&item.ID, &item.Filename, &item.ContentType, &item.SizeBytes, &item.ExpiresAt, &item.PasswordProtected); err != nil {
			return nil, err
		}
		byID[item.ID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []fileLinkSummary
	for _, id := range ids {
		if item, ok := byID[id]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Server) RunFileLinkCleanup(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	s.purgeFileLinks(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.purgeFileLinks(ctx)
		}
	}
}

func (s *Server) purgeFileLinks(ctx context.Context) {
	for range 20 {
		tag, err := s.db.Exec(ctx, `
			DELETE FROM file_links WHERE id IN (
				SELECT id FROM file_links
				WHERE (expires_at IS NOT NULL AND expires_at <= now())
				   OR (committed_at IS NULL AND created_at <= now() - $1::interval)
				LIMIT 100
			)`, fmt.Sprintf("%d seconds", int(uncommittedLinkTTL.Seconds())))
		if err != nil {
			s.log.Error("file link cleanup failed", "err", err)
			return
		}
		if tag.RowsAffected() == 0 {
			return
		}
		s.log.Info("removed expired download links", "count", tag.RowsAffected())
	}
}

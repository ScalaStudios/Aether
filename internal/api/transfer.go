package api

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	defaultMessageBytes       int64 = 50 << 20
	defaultAttachmentBytes    int64 = 20 << 20
	defaultLinkThresholdBytes int64 = 20 << 20
	defaultLinkFileBytes      int64 = 2 << 30
	defaultStorageBytes       int64 = 10 << 30
	maxInlineFiles                  = 20
	maxFileLinks                    = 20
	maxAttachmentBytes              = defaultAttachmentBytes
	minLinkPassword                 = 8
	maxLinkPassword                 = 128
	maxLinkExpiry                   = 5 * 366 * 24 * time.Hour
	uncommittedLinkTTL              = 30 * 24 * time.Hour
	fileChunkBytes                  = 1 << 20
)

type transferLimits struct {
	MessageBytes       int64 `json:"messageBytes"`
	AttachmentBytes    int64 `json:"attachmentBytes"`
	LinkThresholdBytes int64 `json:"linkThresholdBytes"`
	LinkFileBytes      int64 `json:"linkFileBytes"`
	StorageBytes       int64 `json:"storageBytes"`
}

type platformCeilings struct {
	MessageBytes  int64 `json:"messageBytes"`
	LinkFileBytes int64 `json:"linkFileBytes"`
	StorageBytes  int64 `json:"storageBytes"`
}

type transferLimitsView struct {
	transferLimits
	UsedBytes      int64            `json:"usedBytes"`
	MaxInlineFiles int              `json:"maxInlineFiles"`
	MaxFileLinks   int              `json:"maxFileLinks"`
	Platform       platformCeilings `json:"platform"`
}

func (s *Server) platformCeilings() platformCeilings {
	return platformCeilings{MessageBytes: s.cfg.MessageSizeLimit, LinkFileBytes: s.cfg.MaxLinkBytes, StorageBytes: s.cfg.MaxStorageBytes}
}

func defaultTransferLimits(p platformCeilings) transferLimits {
	return (transferLimits{
		MessageBytes:       defaultMessageBytes,
		AttachmentBytes:    defaultAttachmentBytes,
		LinkThresholdBytes: defaultLinkThresholdBytes,
		LinkFileBytes:      defaultLinkFileBytes,
		StorageBytes:       defaultStorageBytes,
	}).effective(p)
}

func (in transferLimits) effective(p platformCeilings) transferLimits {
	msg := clampInt64(in.MessageBytes, 1, p.MessageBytes)
	linkFile := clampInt64(in.LinkFileBytes, 1, p.LinkFileBytes)
	attachment := clampInt64(in.AttachmentBytes, 1, msg)
	threshold := clampInt64(in.LinkThresholdBytes, 1, min(attachment, linkFile))
	return transferLimits{
		MessageBytes:       msg,
		AttachmentBytes:    attachment,
		LinkThresholdBytes: threshold,
		LinkFileBytes:      linkFile,
		StorageBytes:       clampInt64(in.StorageBytes, 0, p.StorageBytes),
	}
}

func clampInt64(n, low, high int64) int64 {
	if high < low {
		return low
	}
	if n < low {
		return low
	}
	if n > high {
		return high
	}
	return n
}

func validateTransferLimits(in transferLimits, p platformCeilings) string {
	switch {
	case in.MessageBytes < 1 || in.MessageBytes > p.MessageBytes:
		return fmt.Sprintf("Message size must be between 1 B and %s, the platform limit.", formatByteSize(p.MessageBytes))
	case in.AttachmentBytes < 1 || in.AttachmentBytes > in.MessageBytes:
		return fmt.Sprintf("Attachment size must be between 1 B and the message size of %s.", formatByteSize(in.MessageBytes))
	case in.LinkThresholdBytes < 1 || in.LinkThresholdBytes > in.AttachmentBytes:
		return fmt.Sprintf("The download-link threshold must be between 1 B and the attachment size of %s.", formatByteSize(in.AttachmentBytes))
	case in.LinkFileBytes < in.LinkThresholdBytes || in.LinkFileBytes > p.LinkFileBytes:
		return fmt.Sprintf("The largest download link must be between %s and %s, the platform limit.", formatByteSize(in.LinkThresholdBytes), formatByteSize(p.LinkFileBytes))
	case in.StorageBytes < 0 || in.StorageBytes > p.StorageBytes:
		return fmt.Sprintf("Download storage must be between 0 B and %s, the platform limit.", formatByteSize(p.StorageBytes))
	default:
		return ""
	}
}

func formatByteSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(n) / 1024
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if value < 10 {
		return fmt.Sprintf("%.1f %s", value, units[unit])
	}
	return fmt.Sprintf("%.0f %s", value, units[unit])
}

func (s *Server) outgoingBodyLimit() int64 {
	n := s.cfg.MessageSizeLimit*2 + (1 << 20)
	if n < 30<<20 {
		n = 30 << 20
	}
	if n > 256<<20 {
		n = 256 << 20
	}
	return n
}

func (s *Server) transferLimits(ctx context.Context, orgID string) (transferLimits, error) {
	limits, err := s.storedTransferLimits(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultTransferLimits(s.platformCeilings()), nil
	}
	if err != nil {
		return transferLimits{}, err
	}
	return limits.effective(s.platformCeilings()), nil
}

func (s *Server) storedTransferLimits(ctx context.Context, orgID string) (transferLimits, error) {
	var limits transferLimits
	err := s.db.QueryRow(ctx, `
		SELECT message_bytes, attachment_bytes, link_threshold_bytes, link_file_bytes, storage_bytes
		FROM organization_transfer_limits WHERE org_id = $1`, orgID).
		Scan(&limits.MessageBytes, &limits.AttachmentBytes, &limits.LinkThresholdBytes, &limits.LinkFileBytes, &limits.StorageBytes)
	return limits, err
}

func (s *Server) linkUsage(ctx context.Context, q rowQuery, orgID string) (int64, error) {
	var used int64
	err := q.QueryRow(ctx, `
		SELECT coalesce(sum(size_bytes), 0) FROM file_links
		WHERE org_id = $1 AND (expires_at IS NULL OR expires_at > now())`, orgID).Scan(&used)
	return used, err
}

type rowQuery interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Server) limitsView(ctx context.Context, orgID string, limits transferLimits) (transferLimitsView, error) {
	used, err := s.linkUsage(ctx, s.db, orgID)
	if err != nil {
		return transferLimitsView{}, err
	}
	return transferLimitsView{
		transferLimits: limits,
		UsedBytes:      used,
		MaxInlineFiles: maxInlineFiles,
		MaxFileLinks:   maxFileLinks,
		Platform:       s.platformCeilings(),
	}, nil
}

func (s *Server) getTransferLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	limits, err := s.transferLimits(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	view, err := s.limitsView(r.Context(), orgID, limits)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) saveTransferLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in transferLimits
	if !decode(w, r, 4<<10, &in) {
		return
	}
	ceilings := s.platformCeilings()
	if msg := validateTransferLimits(in, ceilings); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_limits", msg)
		return
	}
	previous, err := s.transferLimits(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO organization_transfer_limits (org_id, message_bytes, attachment_bytes, link_threshold_bytes, link_file_bytes, storage_bytes, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (org_id) DO UPDATE SET message_bytes = EXCLUDED.message_bytes, attachment_bytes = EXCLUDED.attachment_bytes,
			link_threshold_bytes = EXCLUDED.link_threshold_bytes, link_file_bytes = EXCLUDED.link_file_bytes,
			storage_bytes = EXCLUDED.storage_bytes, updated_at = now()`,
		orgID, in.MessageBytes, in.AttachmentBytes, in.LinkThresholdBytes, in.LinkFileBytes, in.StorageBytes)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "transfer_limits.update", "organization", orgID, map[string]any{"previous": previous, "limits": in})
	view, err := s.limitsView(r.Context(), orgID, in)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) mailboxTransferLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	limits, err := s.transferLimits(r.Context(), m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	view, err := s.limitsView(r.Context(), m.OrgID, limits)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func normalizeAttachments(list []mailpkg.OutgoingAttachment) string {
	for i := range list {
		if msg := normalizeAttachment(&list[i]); msg != "" {
			return msg
		}
	}
	return ""
}

func normalizeAttachment(a *mailpkg.OutgoingAttachment) string {
	a.Filename = strings.TrimSpace(path.Base(strings.ReplaceAll(a.Filename, "\\", "/")))
	if a.Filename == "" || a.Filename == "." || a.Filename == "/" || strings.ContainsAny(a.Filename, "\r\n\x00") || len(a.Filename) > 255 {
		return "One of the attachments has an invalid file name."
	}
	if mt, _, err := mime.ParseMediaType(a.ContentType); err != nil || !strings.Contains(mt, "/") || len(mt) > 200 {
		a.ContentType = "application/octet-stream"
	} else {
		a.ContentType = mt
	}
	return ""
}

func validateAttachments(list []mailpkg.OutgoingAttachment, maxBytes int64) string {
	if msg := normalizeAttachments(list); msg != "" {
		return msg
	}
	if len(list) > maxInlineFiles {
		return fmt.Sprintf("Attach %d files or fewer per message.", maxInlineFiles)
	}
	var total int64
	for _, a := range list {
		total += int64(len(a.Data))
	}
	if total > maxBytes {
		return fmt.Sprintf("Attachments can total at most %s per message.", formatByteSize(maxBytes))
	}
	return ""
}

func partitionAttachments(list []mailpkg.OutgoingAttachment, limits transferLimits) (inline, linked []mailpkg.OutgoingAttachment, msg string) {
	var inlineBytes int64
	for _, a := range list {
		size := int64(len(a.Data))
		fitsInline := size <= limits.LinkThresholdBytes && len(inline) < maxInlineFiles && inlineBytes+size <= limits.AttachmentBytes
		if fitsInline {
			inline = append(inline, a)
			inlineBytes += size
			continue
		}
		if size > limits.LinkFileBytes {
			return nil, nil, fmt.Sprintf("%s is %s, over the %s limit for a file.", displayFilename(a.Filename), formatByteSize(size), formatByteSize(limits.LinkFileBytes))
		}
		linked = append(linked, a)
	}
	if len(linked) > maxFileLinks {
		return nil, nil, fmt.Sprintf("Send %d download links or fewer per message.", maxFileLinks)
	}
	return inline, linked, ""
}

func displayFilename(name string) string {
	if name == "" {
		return "A file"
	}
	return name
}

func messageSizeError(raw []byte, limit int64) string {
	if int64(len(raw)) <= limit {
		return ""
	}
	return fmt.Sprintf("This message is %s, over the %s limit. Send the larger files as download links.", formatByteSize(int64(len(raw))), formatByteSize(limit))
}

func validateLinkPassword(password string) string {
	if len(password) < minLinkPassword || len(password) > maxLinkPassword {
		return fmt.Sprintf("Use a download password between %d and %d characters.", minLinkPassword, maxLinkPassword)
	}
	for _, r := range password {
		if r < 0x20 || r == 0x7f {
			return "The download password cannot contain control characters."
		}
	}
	return ""
}

func validateLinkExpiry(at, notBefore time.Time) string {
	if !at.After(notBefore.Add(time.Minute)) {
		return "Choose an expiry at least a minute after the message is sent."
	}
	if at.After(time.Now().Add(maxLinkExpiry)) {
		return "Expiry has to be within 5 years."
	}
	return ""
}

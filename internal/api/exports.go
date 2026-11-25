package api

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/mail"
)

const (
	maxExportBytes = 2 << 30
	exportLifetime = 7 * 24 * time.Hour
)

type mailboxExport struct {
	ID              string     `json:"id"`
	Format          string     `json:"format"`
	Folder          string     `json:"folder"`
	Since           *time.Time `json:"since"`
	Until           *time.Time `json:"until"`
	IncludeCalendar bool       `json:"includeCalendar"`
	IncludeContacts bool       `json:"includeContacts"`
	Status          string     `json:"status"`
	Error           string     `json:"error"`
	Bytes           int64      `json:"bytes"`
	Messages        int        `json:"messages"`
	MediaType       string     `json:"mediaType"`
	Filename        string     `json:"filename"`
	ExpiresAt       *time.Time `json:"expiresAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
}

type exportJob struct {
	mailboxExport
	MailboxID  string
	OrgID      string
	Address    string
	OwnerID    *string
	ClaimToken string
	ArchiveOID *uint32
}

func scanExport(row pgx.Row, job *exportJob) error {
	return row.Scan(&job.ID, &job.MailboxID, &job.OrgID, &job.Address, &job.OwnerID, &job.Format, &job.Folder, &job.Since, &job.Until,
		&job.IncludeCalendar, &job.IncludeContacts, &job.Status, &job.Error, &job.Bytes, &job.Messages, &job.ArchiveOID, &job.MediaType, &job.Filename,
		&job.ClaimToken, &job.ExpiresAt, &job.CreatedAt, &job.FinishedAt)
}

const exportReturning = `e.id, e.mailbox_id, e.org_id, m.address, m.owner_user_id, e.format, e.folder, e.since, e.until, e.include_calendar, e.include_contacts, e.status, e.error, e.bytes, e.messages, e.archive_oid, e.media_type, e.filename, e.claim_token, e.expires_at, e.created_at, e.finished_at`

func (s *Server) mailboxForExport(w http.ResponseWriter, r *http.Request, p *principal) (*mailbox, bool, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, false, false
	}
	m := &mailbox{}
	err := s.db.QueryRow(r.Context(), `SELECT `+mailboxColumns+` FROM mailboxes WHERE id = $1`, id).Scan(
		&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt, &m.Kind, &m.RetainedAt, &m.Hidden, &m.Protocols, &m.PurgeAfter, &m.OnHold)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return nil, false, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false, false
	}
	admin := p.canAdminOrg(m.OrgID)
	_, _, accessErr := s.accessibleMailbox(r.Context(), m.ID, p.User.ID)
	ownerDuringGrace := m.OwnerUserID != nil && *m.OwnerUserID == p.User.ID && m.PurgeAfter != nil
	switch {
	case accessErr == nil || ownerDuringGrace:
		return m, admin, true
	case admin:
		return m, true, true
	case p.orgRole(m.OrgID) == "":
		notFound(w)
	default:
		forbidden(w)
	}
	return nil, false, false
}

func parseExportBound(v string, end bool) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return time.Time{}, err
	}
	d = d.UTC()
	if end {
		return d.Add(24*time.Hour - time.Nanosecond), nil
	}
	return d, nil
}

func (s *Server) createExport(w http.ResponseWriter, r *http.Request, p *principal) {
	m, admin, ok := s.mailboxForExport(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Format          string `json:"format"`
		Folder          string `json:"folder"`
		Since           string `json:"since"`
		Until           string `json:"until"`
		IncludeCalendar *bool  `json:"includeCalendar"`
		IncludeContacts *bool  `json:"includeContacts"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if in.Format == "" {
		in.Format = "zip"
	}
	in.Folder = strings.TrimSpace(in.Folder)
	since, sinceErr := parseExportBound(in.Since, false)
	until, untilErr := parseExportBound(in.Until, true)
	calendar, contacts := true, true
	if in.IncludeCalendar != nil {
		calendar = *in.IncludeCalendar
	}
	if in.IncludeContacts != nil {
		contacts = *in.IncludeContacts
	}
	switch {
	case in.Format != "zip" && in.Format != "mbox":
		writeError(w, http.StatusUnprocessableEntity, "invalid_format", "Format must be zip or mbox.")
		return
	case sinceErr != nil || untilErr != nil:
		writeError(w, http.StatusUnprocessableEntity, "invalid_range", "Give since and until as a date (YYYY-MM-DD) or an RFC3339 time.")
		return
	case !since.IsZero() && !until.IsZero() && until.Before(since):
		writeError(w, http.StatusUnprocessableEntity, "invalid_range", "The end of the range is before the start.")
		return
	case in.Folder != "" && (len(in.Folder) > 255 || strings.ContainsAny(in.Folder, "\r\n") || mail.SharedFolder(in.Folder)):
		writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "Choose a folder in this mailbox. Shared folders are not part of the export.")
		return
	}
	var running bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM mailbox_exports WHERE mailbox_id = $1 AND actor_user_id = $2 AND status IN ('pending', 'running'))`, m.ID, p.User.ID).Scan(&running); err != nil {
		s.internalError(w, r, err)
		return
	}
	if running {
		writeError(w, http.StatusConflict, "export_running", "An export of this mailbox is already running. Wait for it to finish.")
		return
	}
	var sincePtr, untilPtr *time.Time
	if !since.IsZero() {
		sincePtr = &since
	}
	if !until.IsZero() {
		untilPtr = &until
	}
	job := &exportJob{}
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO mailbox_exports (mailbox_id, org_id, actor_user_id, format, folder, since, until, include_calendar, include_contacts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, format, folder, since, until, include_calendar, include_contacts, status, error, bytes, messages, media_type, filename, expires_at, created_at, finished_at`,
		m.ID, m.OrgID, p.User.ID, in.Format, in.Folder, sincePtr, untilPtr, calendar, contacts).Scan(
		&job.ID, &job.Format, &job.Folder, &job.Since, &job.Until, &job.IncludeCalendar, &job.IncludeContacts, &job.Status, &job.Error, &job.Bytes, &job.Messages, &job.MediaType, &job.Filename, &job.ExpiresAt, &job.CreatedAt, &job.FinishedAt)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.export", "mailbox", m.ID, map[string]any{
		"address": m.Address, "format": in.Format, "folder": in.Folder, "since": in.Since, "until": in.Until,
		"includeCalendar": calendar, "includeContacts": contacts, "admin": admin && (m.OwnerUserID == nil || *m.OwnerUserID != p.User.ID), "exportId": job.ID,
	})
	wake(s.exportWake)
	writeJSON(w, http.StatusAccepted, job.mailboxExport)
}

func (s *Server) listExports(w http.ResponseWriter, r *http.Request, p *principal) {
	m, admin, ok := s.mailboxForExport(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT id, format, folder, since, until, include_calendar, include_contacts, status, error, bytes, messages, media_type, filename, expires_at, created_at, finished_at
		FROM mailbox_exports WHERE mailbox_id = $1 AND ($2 OR actor_user_id = $3) ORDER BY created_at DESC LIMIT 20`, m.ID, admin, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mailboxExport, error) {
		var e mailboxExport
		err := row.Scan(&e.ID, &e.Format, &e.Folder, &e.Since, &e.Until, &e.IncludeCalendar, &e.IncludeContacts, &e.Status, &e.Error, &e.Bytes, &e.Messages, &e.MediaType, &e.Filename, &e.ExpiresAt, &e.CreatedAt, &e.FinishedAt)
		return e, err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) downloadExport(w http.ResponseWriter, r *http.Request, p *principal) {
	m, admin, ok := s.mailboxForExport(w, r, p)
	if !ok {
		return
	}
	exportID, ok := pathID(w, r, "exportId")
	if !ok {
		return
	}
	var e mailboxExport
	var oid *uint32
	err := s.db.QueryRow(r.Context(), `
		SELECT id, format, folder, since, until, include_calendar, include_contacts, status, error, bytes, messages, archive_oid, media_type, filename, expires_at, created_at, finished_at
		FROM mailbox_exports WHERE id = $1 AND mailbox_id = $2 AND ($3 OR actor_user_id = $4)`, exportID, m.ID, admin, p.User.ID).Scan(
		&e.ID, &e.Format, &e.Folder, &e.Since, &e.Until, &e.IncludeCalendar, &e.IncludeContacts, &e.Status, &e.Error, &e.Bytes, &e.Messages, &oid, &e.MediaType, &e.Filename, &e.ExpiresAt, &e.CreatedAt, &e.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if e.Status == "expired" || (e.ExpiresAt != nil && !e.ExpiresAt.After(time.Now())) {
		writeError(w, http.StatusGone, "export_expired", "This download link has expired.")
		return
	}
	if e.Status != "ready" || oid == nil {
		writeError(w, http.StatusConflict, "export_not_ready", "The export is not ready yet.")
		return
	}
	w.Header().Set("Content-Type", e.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", e.Filename))
	w.Header().Set("Content-Length", strconv.FormatInt(e.Bytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		los := tx.LargeObjects()
		obj, err := los.Open(r.Context(), *oid, pgx.LargeObjectModeRead)
		if err != nil {
			return err
		}
		defer obj.Close()
		_, err = io.Copy(w, obj)
		return err
	})
	if err != nil {
		s.log.Warn("export download failed", "export", e.ID, "err", err)
	}
}

func (s *Server) RunMailboxMaintenance(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		s.sweepExports(ctx)
		s.runNextExport(ctx)
		s.purgeDueMailboxes(ctx)
		s.expireExports(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.exportWake:
		}
	}
}

func (s *Server) sweepExports(ctx context.Context) {
	if _, err := s.db.Exec(ctx, `UPDATE mailbox_exports SET status = 'pending', claim_token = NULL WHERE status = 'running' AND heartbeat_at < now() - interval '2 minutes'`); err != nil && ctx.Err() == nil {
		s.log.Error("resetting stalled mailbox exports failed", "err", err)
	}
}

func (s *Server) runNextExport(ctx context.Context) {
	job := &exportJob{}
	err := s.db.QueryRow(ctx, `
		WITH claimed AS (
			SELECT id FROM mailbox_exports WHERE status = 'pending' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE mailbox_exports e SET status = 'running', claim_token = gen_random_uuid(), heartbeat_at = now()
		FROM claimed, mailboxes m
		WHERE e.id = claimed.id AND m.id = e.mailbox_id
		RETURNING `+exportReturning).Scan(
		&job.ID, &job.MailboxID, &job.OrgID, &job.Address, &job.OwnerID, &job.Format, &job.Folder, &job.Since, &job.Until,
		&job.IncludeCalendar, &job.IncludeContacts, &job.Status, &job.Error, &job.Bytes, &job.Messages, &job.ArchiveOID, &job.MediaType, &job.Filename,
		&job.ClaimToken, &job.ExpiresAt, &job.CreatedAt, &job.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("claiming a mailbox export failed", "err", err)
		}
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	s.buildExport(runCtx, job)
}

func (s *Server) buildExport(ctx context.Context, job *exportJob) {
	hb, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-hb.Done():
				return
			case <-ticker.C:
				s.db.Exec(context.Background(), `UPDATE mailbox_exports SET heartbeat_at = now() WHERE id = $1 AND claim_token = $2 AND status = 'running'`, job.ID, job.ClaimToken)
			}
		}
	}()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.failExport(job, err.Error())
		return
	}
	defer tx.Rollback(ctx)
	los := tx.LargeObjects()
	oid, err := los.Create(ctx, 0)
	if err != nil {
		s.failExport(job, "The export could not be stored.")
		return
	}
	obj, err := los.Open(ctx, oid, pgx.LargeObjectModeWrite)
	if err != nil {
		s.failExport(job, "The export could not be stored.")
		return
	}
	counter := &limitedWriter{w: obj, limit: maxExportBytes}
	messages, media, filename, err := s.writeExport(ctx, job, counter)
	obj.Close()
	if err != nil {
		s.failExport(job, exportFailure(err))
		return
	}
	tag, err := tx.Exec(ctx, `
		UPDATE mailbox_exports SET status = 'ready', archive_oid = $3, bytes = $4, messages = $5, media_type = $6, filename = $7, expires_at = now() + interval '7 days', finished_at = now()
		WHERE id = $1 AND claim_token = $2 AND status = 'running'`, job.ID, job.ClaimToken, oid, counter.n, messages, media, filename)
	if err != nil {
		s.failExport(job, "The export could not be stored.")
		return
	}
	if tag.RowsAffected() == 0 {
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.failExport(job, "The export could not be stored.")
	}
}

type limitedWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.n+int64(len(p)) > w.limit {
		return 0, errExportTooLarge
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	return n, err
}

var errExportTooLarge = errors.New("export is larger than 2 GiB")

func exportFailure(err error) string {
	switch {
	case errors.Is(err, errExportTooLarge):
		return "The export is larger than 2 GiB. Export one folder, or a shorter date range."
	case errors.Is(err, context.DeadlineExceeded):
		return "The export did not finish within 6 hours. Export one folder, or a shorter date range."
	default:
		return "The export stopped: " + truncate(err.Error(), 400)
	}
}

func (s *Server) failExport(job *exportJob, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.db.Exec(ctx, `UPDATE mailbox_exports SET status = 'failed', error = $3, finished_at = now() WHERE id = $1 AND claim_token = $2 AND status = 'running'`, job.ID, job.ClaimToken, message); err != nil {
		s.log.Error("recording a failed mailbox export failed", "export", job.ID, "err", err)
	}
}

func (s *Server) writeExport(ctx context.Context, job *exportJob, w io.Writer) (int, string, string, error) {
	safeAddress := strings.NewReplacer("@", "-", "/", "-", "\\", "-", "\"", "").Replace(job.Address)
	wantPIM := job.OwnerID != nil && (job.IncludeCalendar || job.IncludeContacts)
	if job.Format == "mbox" && !wantPIM {
		n, err := s.writeMbox(ctx, job, w)
		return n, "application/mbox", safeAddress + ".mbox", err
	}
	zw := zip.NewWriter(w)
	var n int
	var err error
	if job.Format == "mbox" {
		entry, createErr := zw.Create("mailbox.mbox")
		if createErr != nil {
			return 0, "", "", createErr
		}
		n, err = s.writeMbox(ctx, job, entry)
	} else {
		n, err = s.writeEML(ctx, job, zw)
	}
	if err != nil {
		zw.Close()
		return n, "", "", err
	}
	if wantPIM {
		if err := s.writePIM(ctx, *job.OwnerID, job.IncludeCalendar, job.IncludeContacts, zw); err != nil {
			zw.Close()
			return n, "", "", err
		}
	}
	if err := zw.Close(); err != nil {
		return n, "", "", err
	}
	return n, "application/zip", safeAddress + ".zip", nil
}

func (s *Server) writeMbox(ctx context.Context, job *exportJob, w io.Writer) (int, error) {
	n := 0
	err := s.mail.ExportMessages(ctx, job.Address, exportFilter(job), func(msg mail.RawMessage) error {
		n++
		return mail.WriteMbox(w, msg.Internal, msg.Body)
	})
	return n, err
}

func (s *Server) writeEML(ctx context.Context, job *exportJob, zw *zip.Writer) (int, error) {
	n := 0
	err := s.mail.ExportMessages(ctx, job.Address, exportFilter(job), func(msg mail.RawMessage) error {
		name, err := mail.MailExportPath(msg.Folder, msg.UID)
		if err != nil {
			return err
		}
		entry, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := entry.Write(msg.Body); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

func exportFilter(job *exportJob) mail.ExportFilter {
	f := mail.ExportFilter{Folder: job.Folder}
	if job.Since != nil {
		f.Since = *job.Since
	}
	if job.Until != nil {
		f.Until = *job.Until
	}
	return f
}

func (s *Server) writePIM(ctx context.Context, userID string, calendar, contacts bool, zw *zip.Writer) error {
	if calendar {
		rows, err := s.db.Query(ctx, `SELECT slug FROM dav_collections WHERE user_id = $1 AND kind = 'calendar' ORDER BY slug`, userID)
		if err != nil {
			return err
		}
		slugs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, slug := range slugs {
			body, err := s.calendarICS(ctx, userID, slug)
			if err != nil {
				return err
			}
			entry, err := zw.Create("calendars/" + slug + ".ics")
			if err != nil {
				return err
			}
			if _, err := io.WriteString(entry, body); err != nil {
				return err
			}
		}
	}
	if contacts {
		rows, err := s.db.Query(ctx, `
			SELECT c.slug, o.data FROM dav_collections c
			LEFT JOIN dav_objects o ON o.collection_id = c.id
			WHERE c.user_id = $1 AND c.kind = 'addressbook'
			ORDER BY c.slug, o.name`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		var slug, current string
		var data *string
		var buf strings.Builder
		flush := func() error {
			if current == "" {
				return nil
			}
			entry, err := zw.Create("contacts/" + current + ".vcf")
			if err != nil {
				return err
			}
			_, err = io.WriteString(entry, buf.String())
			buf.Reset()
			return err
		}
		for rows.Next() {
			if err := rows.Scan(&slug, &data); err != nil {
				return err
			}
			if slug != current {
				if err := flush(); err != nil {
					return err
				}
				current = slug
			}
			if data != nil && *data != "" {
				buf.WriteString(*data)
				if !strings.HasSuffix(*data, "\n") {
					buf.WriteByte('\n')
				}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return flush()
	}
	return nil
}

func (s *Server) expireExports(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT id, archive_oid FROM mailbox_exports WHERE status = 'ready' AND expires_at IS NOT NULL AND expires_at <= now() LIMIT 20`)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("listing expired mailbox exports failed", "err", err)
		}
		return
	}
	type item struct {
		id  string
		oid uint32
	}
	var due []item
	for rows.Next() {
		var it item
		var oid *uint32
		if err := rows.Scan(&it.id, &oid); err != nil {
			rows.Close()
			return
		}
		if oid != nil {
			it.oid = *oid
		}
		due = append(due, it)
	}
	rows.Close()
	for _, it := range due {
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if it.oid != 0 {
				los := tx.LargeObjects()
				if err := los.Unlink(ctx, it.oid); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `UPDATE mailbox_exports SET status = 'expired', archive_oid = NULL WHERE id = $1 AND status = 'ready'`, it.id)
			return err
		})
		if err != nil && ctx.Err() == nil {
			s.log.Warn("expiring a mailbox export failed", "export", it.id, "err", err)
		}
	}
}

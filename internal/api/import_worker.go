package api

import (
	"context"
	"errors"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/mail"
)

var importPermitted = `m.active AND d.active AND d.verified_at IS NOT NULL AND
	(j.user_id IS NULL OR (EXISTS (SELECT 1 FROM users WHERE id = j.user_id AND status = 'active')
	AND ` + mailboxAccessLevel("j.user_id") + ` IN ('owner', 'full')))`

func (s *Server) dispatchImports(ctx context.Context) {
	if _, err := s.db.Exec(ctx, `UPDATE mail_import_schedules j SET credential = NULL,
		status = CASE WHEN j.ends_at <= now() THEN 'expired' ELSE 'stopped' END
		FROM mailboxes m JOIN domains d ON d.id = m.domain_id WHERE j.mailbox_id = m.id AND j.credential IS NOT NULL
		AND (j.ends_at <= now() OR NOT (`+importPermitted+`))`); err != nil {
		s.log.Warn("expiring mail import schedules failed", "err", err)
		return
	}
	rows, err := s.db.Query(ctx, `SELECT j.id, j.mailbox_id FROM mail_import_schedules j JOIN mailboxes m ON m.id = j.mailbox_id
		WHERE j.credential IS NOT NULL AND j.next_run_at <= now()
		  AND NOT EXISTS (SELECT 1 FROM organization_disabled_features f WHERE f.org_id = m.org_id AND f.feature = 'mailImport')
		ORDER BY j.next_run_at, j.id`)
	if err != nil {
		s.log.Warn("listing due mail imports failed", "err", err)
		return
	}
	type dueImport struct{ ID, MailboxID string }
	due, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dueImport])
	if err != nil {
		s.log.Warn("reading due mail imports failed", "err", err)
		return
	}
	for _, job := range due {
		select {
		case s.importSlots <- struct{}{}:
		default:
			return
		}
		conn, err := s.lockImport(ctx, job.MailboxID)
		if conn == nil {
			<-s.importSlots
			if err != nil {
				s.log.Warn("locking mail import failed", "err", err)
				return
			}
			continue
		}
		go func() {
			defer func() { <-s.importSlots }()
			defer closeImportLock(conn)
			if err := s.runScheduledImport(ctx, conn, job.ID, job.MailboxID); err != nil && ctx.Err() == nil {
				s.log.Warn("scheduled mail import failed", "schedule", job.ID, "err", err)
			}
		}()
	}
}

func (s *Server) runScheduledImport(ctx context.Context, conn *pgx.Conn, id, mailboxID string) error {
	var in importScheduleInput
	var userID *string
	var credential []byte
	var address, orgID string
	err := conn.QueryRow(ctx, `SELECT j.user_id, j.host, j.port, j.security, j.username, j.credential,
		j.ends_at, j.sync_flags, j.sync_deletions, m.address, m.org_id
		FROM mail_import_schedules j JOIN mailboxes m ON m.id = j.mailbox_id JOIN domains d ON d.id = m.domain_id
		WHERE j.id = $1 AND j.credential IS NOT NULL AND j.next_run_at <= now()
		AND (j.ends_at IS NULL OR j.ends_at > now()) AND `+importPermitted, id).
		Scan(&userID, &in.Host, &in.Port, &in.Security, &in.Username, &credential, &in.EndsAt, &in.SyncFlags, &in.SyncDeletions, &address, &orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	limits, err := s.transferLimits(ctx, orgID)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(24 * time.Hour)
	if in.EndsAt != nil && in.EndsAt.Before(deadline) {
		deadline = *in.EndsAt
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	job, err := insertImportRun(runCtx, conn, mailboxID, userID, &id, in.importInput)
	if err != nil {
		return err
	}
	password, err := s.box.Open(credential, "mail-import:"+id)
	if err != nil {
		if _, saveErr := conn.Exec(ctx, `UPDATE mail_imports SET status = 'failed', error = 'Aether could not decrypt the source password.', finished_at = now() WHERE id = $1`, job.ID); saveErr != nil {
			return saveErr
		}
	} else {
		in.Password = string(password)
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			s.watchImportAccess(runCtx, func(error) { cancel() }, conn, `SELECT j.credential IS NOT NULL AND `+importPermitted+`
				FROM mail_import_schedules j JOIN mailboxes m ON m.id = j.mailbox_id JOIN domains d ON d.id = m.domain_id WHERE j.id = $1`, id)
		}()
		s.runImport(runCtx, job.ID, address, s.importSource(in.importInput), limits.MessageBytes, s.importSync(id, in), true)
		cancel()
		<-watched
	}
	_, err = conn.Exec(ctx, `UPDATE mail_import_schedules SET next_run_at = now() + make_interval(mins => interval_minutes)
		WHERE id = $1 AND credential IS NOT NULL`, id)
	return err
}

func (s *Server) watchImportAccess(ctx context.Context, cancel context.CancelCauseFunc, conn *pgx.Conn, query, id string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		var active bool
		checkCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err := conn.QueryRow(checkCtx, query, id).Scan(&active)
		finish()
		if err != nil {
			cancel(nil)
			return
		}
		if !active {
			cancel(errImportAccess)
			return
		}
	}
}

func (s *Server) importSync(id string, in importScheduleInput) *mail.ImportSync {
	if !in.SyncFlags && !in.SyncDeletions {
		return nil
	}
	return &mail.ImportSync{
		Flags: in.SyncFlags, Deletions: in.SyncDeletions,
		Load: func(ctx context.Context, f mail.ImportFolder) (map[imap.UID]imap.UID, error) {
			rows, err := s.db.Query(ctx, `SELECT source_uid, target_uid FROM mail_import_copies
				WHERE schedule_id = $1 AND source_folder = $2 AND source_validity = $3 AND target_folder = $4 AND target_validity = $5`,
				id, f.Source, f.SourceValidity, f.Target, f.TargetValidity)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			copies := map[imap.UID]imap.UID{}
			for rows.Next() {
				var source, target imap.UID
				if err := rows.Scan(&source, &target); err != nil {
					return nil, err
				}
				copies[source] = target
			}
			return copies, rows.Err()
		},
		Save: func(ctx context.Context, f mail.ImportFolder, source, target imap.UID) error {
			dbCtx, cancel := cleanupContext(ctx)
			defer cancel()
			_, err := s.db.Exec(dbCtx, `INSERT INTO mail_import_copies
				(schedule_id, source_folder, source_validity, target_folder, target_validity, source_uid, target_uid)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (schedule_id, source_folder, source_validity, source_uid, target_folder, target_validity)
				DO UPDATE SET target_uid = EXCLUDED.target_uid`, id, f.Source, f.SourceValidity, f.Target, f.TargetValidity, source, target)
			return err
		},
	}
}

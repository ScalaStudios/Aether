package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type importScheduleInput struct {
	importInput
	MailboxID       string     `json:"mailboxId"`
	IntervalMinutes int        `json:"intervalMinutes"`
	EndsAt          *time.Time `json:"endsAt"`
	SyncFlags       bool       `json:"syncFlags"`
	SyncDeletions   bool       `json:"syncDeletions"`
}

type importSchedule struct {
	ID              string      `json:"id"`
	MailboxID       string      `json:"mailboxId"`
	Address         string      `json:"address"`
	Host            string      `json:"host"`
	Username        string      `json:"username"`
	Status          string      `json:"status"`
	IntervalMinutes int         `json:"intervalMinutes"`
	EndsAt          *time.Time  `json:"endsAt"`
	NextRunAt       *time.Time  `json:"nextRunAt"`
	SyncFlags       bool        `json:"syncFlags"`
	SyncDeletions   bool        `json:"syncDeletions"`
	LatestRun       *mailImport `json:"latestRun"`
}

func (s *Server) startMailboxImportSchedule(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var in importScheduleInput
	if decode(w, r, 8<<10, &in) {
		in.MailboxID = m.ID
		s.createImportSchedules(w, r, p, m.OrgID, &p.User.ID, []importScheduleInput{in})
	}
}

func (s *Server) startBulkImports(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		Imports []importScheduleInput `json:"imports"`
	}
	if decode(w, r, 512<<10, &in) {
		s.createImportSchedules(w, r, p, orgID, nil, in.Imports)
	}
}

func (s *Server) createImportSchedules(w http.ResponseWriter, r *http.Request, p *principal, orgID string, userID *string, inputs []importScheduleInput) {
	if !s.requireFeature(w, r, orgID, featureMailImport) {
		return
	}
	if len(inputs) < 1 || len(inputs) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_batch", "Choose between 1 and 100 mailboxes.")
		return
	}
	for i := range inputs {
		in := &inputs[i]
		in.MailboxID = strings.ToLower(in.MailboxID)
		if !uuidPattern.MatchString(in.MailboxID) {
			notFound(w)
			return
		}
		if !in.valid(w) {
			return
		}
		if in.IntervalMinutes < 1 || in.IntervalMinutes > 10080 || (in.EndsAt != nil && !in.EndsAt.After(time.Now())) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_schedule", "Set a repeat interval of 1 to 10080 minutes and choose a future end time if needed.")
			return
		}
	}
	ctx := r.Context()
	out := make([]importSchedule, 0, len(inputs))
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, in := range inputs {
			job := importSchedule{
				ID: uuid.NewString(), MailboxID: in.MailboxID, Host: in.Host, Username: in.Username, Status: "scheduled",
				IntervalMinutes: in.IntervalMinutes, EndsAt: in.EndsAt, SyncFlags: in.SyncFlags, SyncDeletions: in.SyncDeletions,
			}
			err := tx.QueryRow(ctx, `INSERT INTO mail_import_schedules
				(id, mailbox_id, user_id, host, port, security, username, credential, interval_minutes, ends_at, sync_flags, sync_deletions)
				SELECT $1, m.id, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
				FROM mailboxes m JOIN domains d ON d.id = m.domain_id
				WHERE m.id = $2 AND m.org_id = $3 AND m.active AND d.active AND d.verified_at IS NOT NULL
				RETURNING next_run_at, (SELECT address FROM mailboxes WHERE id = $2)`,
				job.ID, job.MailboxID, orgID, userID, in.Host, in.Port, in.Security, in.Username, s.box.Seal([]byte(in.Password), "mail-import:"+job.ID),
				in.IntervalMinutes, in.EndsAt, in.SyncFlags, in.SyncDeletions).Scan(&job.NextRunAt, &job.Address)
			if err != nil {
				return err
			}
			out = append(out, job)
		}
		return nil
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w)
	case isUniqueViolation(err):
		writeError(w, http.StatusConflict, "import_scheduled", "A selected mailbox appears twice or already has an active import schedule.")
	case err != nil:
		s.internalError(w, r, err)
	default:
		for _, job := range out {
			s.audit(ctx, r, p, &orgID, "mail_import.schedule", "mailbox", job.MailboxID, map[string]any{"scheduleId": job.ID, "host": job.Host})
		}
		writeJSON(w, http.StatusAccepted, out)
	}
}

func (s *Server) listOrgImportSchedules(w http.ResponseWriter, r *http.Request, p *principal) {
	if orgID, ok := s.orgFromPath(w, r, p, true); ok {
		s.listImportSchedules(w, r, "m.org_id", orgID)
	}
}

func (s *Server) listMailboxImportSchedules(w http.ResponseWriter, r *http.Request, p *principal) {
	if m, ok := s.ownedMailbox(w, r, p); ok {
		s.listImportSchedules(w, r, "m.id", m.ID)
	}
}

func (s *Server) listImportSchedules(w http.ResponseWriter, r *http.Request, field, id string) {
	rows, err := s.db.Query(r.Context(), `SELECT j.id, j.mailbox_id, m.address, j.host, j.username, j.status, j.interval_minutes, j.ends_at,
		CASE WHEN j.credential IS NOT NULL AND (j.ends_at IS NULL OR j.next_run_at < j.ends_at) THEN j.next_run_at END,
		j.sync_flags, j.sync_deletions
		FROM mail_import_schedules j JOIN mailboxes m ON m.id = j.mailbox_id
		WHERE `+field+` = $1 ORDER BY (j.credential IS NULL), j.created_at DESC`, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (importSchedule, error) {
		var j importSchedule
		err := row.Scan(&j.ID, &j.MailboxID, &j.Address, &j.Host, &j.Username, &j.Status, &j.IntervalMinutes, &j.EndsAt, &j.NextRunAt, &j.SyncFlags, &j.SyncDeletions)
		return j, err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err = s.db.Query(r.Context(), `SELECT `+importRunColumns+` FROM mail_import_schedules j
		JOIN mailboxes m ON m.id = j.mailbox_id
		JOIN LATERAL (SELECT * FROM mail_imports WHERE schedule_id = j.id ORDER BY created_at DESC LIMIT 1) i ON true
		WHERE `+field+` = $1`, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	runs, err := pgx.CollectRows(rows, scanImportRun)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	latest := make(map[string]*mailImport, len(runs))
	for i := range runs {
		latest[*runs[i].ScheduleID] = &runs[i]
	}
	for i := range out {
		out[i].LatestRun = latest[out[i].ID]
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) orgImportRuns(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var found bool
	err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM mail_import_schedules j JOIN mailboxes m ON m.id = j.mailbox_id
		WHERE j.id = $1 AND m.org_id = $2)`, id, orgID).Scan(&found)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !found {
		notFound(w)
		return
	}
	s.writeImportRuns(w, r, `i.schedule_id = $1 AND EXISTS (SELECT 1 FROM mailboxes WHERE id = i.mailbox_id AND org_id = $2)`, id, orgID)
}

func (s *Server) stopBulkImports(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		IDs []string `json:"ids"`
	}
	if decode(w, r, 8<<10, &in) {
		s.stopImportSchedules(w, r, p, orgID, "m.org_id", orgID, in.IDs)
	}
}

func (s *Server) stopMailboxImport(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	if id, ok := pathID(w, r, "id"); ok {
		s.stopImportSchedules(w, r, p, m.OrgID, "m.id", m.ID, []string{id})
	}
}

func (s *Server) stopImportSchedules(w http.ResponseWriter, r *http.Request, p *principal, orgID, field, id string, ids []string) {
	if len(ids) < 1 || len(ids) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_batch", "Choose between 1 and 100 import schedules.")
		return
	}
	for i, job := range ids {
		ids[i] = strings.ToLower(job)
		if !uuidPattern.MatchString(ids[i]) {
			notFound(w)
			return
		}
	}
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE mail_import_schedules j
			SET credential = NULL, status = CASE WHEN j.credential IS NULL THEN j.status ELSE 'stopped' END
			FROM mailboxes m WHERE m.id = j.mailbox_id AND `+field+` = $1 AND j.id = ANY($2::uuid[])`, id, ids)
		if err == nil && tag.RowsAffected() != int64(len(ids)) {
			return pgx.ErrNoRows
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, id := range ids {
		s.audit(r.Context(), r, p, &orgID, "mail_import.stop", "import_schedule", id, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

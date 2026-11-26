package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/mail"
)

type mailImport struct {
	ScheduleID *string             `json:"scheduleId"`
	ID         string              `json:"id"`
	Host       string              `json:"host"`
	Username   string              `json:"username"`
	Status     string              `json:"status"`
	Progress   mail.ImportProgress `json:"progress"`
	Error      string              `json:"error"`
	CreatedAt  time.Time           `json:"createdAt"`
	FinishedAt *time.Time          `json:"finishedAt"`
}

func (s *Server) listImports(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok || !s.requireFeature(w, r, m.OrgID, featureMailImport) {
		return
	}
	s.writeImportRuns(w, r, "i.mailbox_id = $1", m.ID)
}

const importRunColumns = `i.id, i.schedule_id, i.host, i.username,
	CASE WHEN i.status = 'running' AND NOT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory'
		AND granted AND classid = 37136 AND objsubid = 2 AND objid::bigint = (hashtext(i.mailbox_id::text)::bigint & 4294967295)
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database()))
	THEN 'interrupted' ELSE i.status END, i.progress, i.error, i.created_at, i.finished_at`

func scanImportRun(row pgx.CollectableRow) (mailImport, error) {
	var i mailImport
	var progress []byte
	err := row.Scan(&i.ID, &i.ScheduleID, &i.Host, &i.Username, &i.Status, &progress, &i.Error, &i.CreatedAt, &i.FinishedAt)
	json.Unmarshal(progress, &i.Progress)
	return i, err
}

func (s *Server) writeImportRuns(w http.ResponseWriter, r *http.Request, filter string, args ...any) {
	rows, err := s.db.Query(r.Context(), `SELECT `+importRunColumns+` FROM mail_imports i WHERE `+filter+` ORDER BY i.created_at DESC LIMIT 20`, args...)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scanImportRun)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

type importInput struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (in *importInput) valid(w http.ResponseWriter) bool {
	in.Host = strings.ToLower(strings.TrimSpace(in.Host))
	in.Username = strings.TrimSpace(in.Username)
	if in.Port == 0 {
		in.Port = 993
	}
	if !domainPattern.MatchString(in.Host) || in.Port < 1 || in.Port > 65535 || (in.Security != "tls" && in.Security != "starttls") {
		writeError(w, http.StatusUnprocessableEntity, "invalid_server", "Enter the other provider's IMAP server name and port.")
		return false
	}
	if in.Username == "" || in.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_credentials", "Enter the username and password for the other account.")
		return false
	}
	return true
}

func (s *Server) importSource(in importInput) mail.ImportSource {
	dialer := publicDialer(s.cfg.WebhookAllowPrivate)
	return mail.ImportSource{Host: in.Host, Port: in.Port, StartTLS: in.Security == "starttls", Username: in.Username, Password: in.Password, Dial: dialer.DialContext}
}

func (s *Server) startImport(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok || !s.requireFeature(w, r, m.OrgID, featureMailImport) {
		return
	}
	var in importInput
	if !decode(w, r, 8<<10, &in) || !in.valid(w) {
		return
	}
	limits, err := s.transferLimits(r.Context(), m.OrgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	src := s.importSource(in)
	checkCtx, cancelCheck := context.WithTimeout(r.Context(), 30*time.Second)
	err = mail.CheckImportSource(checkCtx, src)
	cancelCheck()
	if err != nil {
		message := "Aether could not connect to " + in.Host + ". Check the server name, port and security, and that the provider allows IMAP access."
		if errors.Is(err, mail.ErrImportLogin) {
			message = "The other provider rejected the username or password. Many providers, such as Gmail, iCloud and Yahoo, need an app password for IMAP."
		}
		writeError(w, http.StatusUnprocessableEntity, "import_connect_failed", message)
		return
	}

	conn, err := s.lockImport(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if conn == nil {
		writeError(w, http.StatusConflict, "import_running", "An import into this mailbox is already running.")
		return
	}
	job, err := insertImportRun(r.Context(), conn, m.ID, &p.User.ID, nil, in)
	if err != nil {
		closeImportLock(conn)
		s.internalError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	go func() {
		defer cancel()
		defer closeImportLock(conn)
		runCtx, stop := context.WithCancelCause(ctx)
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			s.watchImportAccess(runCtx, stop, conn, `SELECT `+importPermitted+` FROM mail_imports j
				JOIN mailboxes m ON m.id = j.mailbox_id JOIN domains d ON d.id = m.domain_id WHERE j.id = $1`, job.ID)
		}()
		s.runImport(runCtx, job.ID, m.Address, src, limits.MessageBytes, nil, false)
		stop(nil)
		<-watched
	}()
	s.audit(r.Context(), r, p, &m.OrgID, "mail_import.start", "mailbox", m.ID, map[string]any{"host": in.Host})
	writeJSON(w, http.StatusAccepted, job)
}

func importCancelKey(id string) string {
	return "aether:import-cancel:" + id
}

func (s *Server) requestImportCancel(ctx context.Context, ids ...string) error {
	for _, id := range ids {
		if err := s.kv.Set(ctx, importCancelKey(id), "1", 25*time.Hour).Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) watchImportCancel(ctx context.Context, cancel context.CancelFunc, id string, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if n, err := s.kv.Exists(ctx, importCancelKey(id)).Result(); err == nil && n > 0 {
			cancel()
			return
		}
	}
}

func (s *Server) cancelOrgImports(ctx context.Context, orgID string) error {
	rows, err := s.db.Query(ctx, `SELECT i.id FROM mail_imports i JOIN mailboxes m ON m.id = i.mailbox_id WHERE m.org_id = $1 AND i.status = 'running'`, orgID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	return s.requestImportCancel(ctx, ids...)
}

func (s *Server) lockImport(ctx context.Context, mailboxID string) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, s.db.Config().ConnConfig)
	if err != nil {
		return nil, err
	}
	var locked bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(37136, hashtext($1))`, mailboxID).Scan(&locked)
	if err != nil || !locked {
		closeImportLock(conn)
		return nil, err
	}
	return conn, nil
}

func closeImportLock(conn *pgx.Conn) {
	ctx, cancel := cleanupContext(context.Background())
	defer cancel()
	conn.Close(ctx)
}

func insertImportRun(ctx context.Context, conn *pgx.Conn, mailboxID string, userID, scheduleID *string, in importInput) (mailImport, error) {
	var job mailImport
	if _, err := conn.Exec(ctx, `UPDATE mail_imports SET status = 'interrupted', finished_at = now() WHERE mailbox_id = $1 AND status = 'running'`, mailboxID); err != nil {
		return job, err
	}
	err := conn.QueryRow(ctx, `INSERT INTO mail_imports (mailbox_id, user_id, schedule_id, host, username) VALUES ($1, $2, $3, $4, $5)
		RETURNING id, schedule_id, host, username, status, error, created_at`, mailboxID, userID, scheduleID, in.Host, in.Username).
		Scan(&job.ID, &job.ScheduleID, &job.Host, &job.Username, &job.Status, &job.Error, &job.CreatedAt)
	return job, err
}

var errImportAccess = errors.New("import access revoked")

func (s *Server) runImport(ctx context.Context, id, address string, src mail.ImportSource, maxBytes int64, sync *mail.ImportSync, recurring bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.watchImportCancel(ctx, cancel, id, 2*time.Second)
	var latest mail.ImportProgress
	var last time.Time
	err := s.mail.Import(ctx, address, src, maxBytes, func(p mail.ImportProgress) {
		latest = p
		if time.Since(last) < 2*time.Second {
			return
		}
		last = time.Now()
		body, _ := json.Marshal(p)
		dbCtx, done := context.WithTimeout(ctx, 2*time.Second)
		defer done()
		s.db.Exec(dbCtx, `UPDATE mail_imports SET progress = $2 WHERE id = $1`, id, body)
	}, sync)
	status, message := "done", ""
	switch {
	case errors.Is(context.Cause(ctx), errImportAccess):
		status, message = "cancelled", "The import stopped because the person who started it no longer has access to this mailbox."
	case errors.Is(err, context.Canceled):
		status = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		status, message = "failed", "The import reached its time limit."
	case errors.Is(err, mail.ErrImportLogin):
		status, message = "failed", "The other provider rejected the username or password."
	case err != nil:
		status, message = "failed", importFailure(err)
		s.log.Warn("mail import failed", "import", id, "err", err)
	}
	if recurring && status == "failed" {
		if !errors.Is(err, mail.ErrImportLogin) && !errors.Is(err, context.DeadlineExceeded) {
			message = "The connection or copy did not finish."
		}
		message += " Aether retries while the schedule is active."
	}
	dbCtx, done := cleanupContext(ctx)
	defer done()
	body, _ := json.Marshal(latest)
	if _, err := s.db.Exec(dbCtx, `UPDATE mail_imports SET status = $2, error = $3, progress = $4, finished_at = now() WHERE id = $1`, id, status, message, body); err != nil {
		s.log.Warn("saving mail import result failed", "import", id, "err", err)
	}
}

func importFailure(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, errBlockedAddress) {
		return "The connection to the other provider was lost. Start the import again; messages already copied are skipped."
	}
	return "The import stopped: " + err.Error() + ". Start it again; messages already copied are skipped."
}

func (s *Server) cancelImport(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok || !s.requireFeature(w, r, m.OrgID, featureMailImport) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var running bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM mail_imports WHERE id = $1 AND mailbox_id = $2 AND status = 'running')`, id, m.ID).Scan(&running); err != nil {
		s.internalError(w, r, err)
		return
	}
	if !running {
		notFound(w)
		return
	}
	if err := s.requestImportCancel(r.Context(), id); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

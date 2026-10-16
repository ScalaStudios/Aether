package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/mail"
)

type mailImport struct {
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
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, host, username, status, progress, error, created_at, finished_at FROM mail_imports WHERE mailbox_id = $1 ORDER BY created_at DESC LIMIT 20`, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mailImport, error) {
		var i mailImport
		var progress []byte
		err := row.Scan(&i.ID, &i.Host, &i.Username, &i.Status, &progress, &i.Error, &i.CreatedAt, &i.FinishedAt)
		json.Unmarshal(progress, &i.Progress)
		if _, running := s.imports.Load(i.ID); i.Status == "running" && !running {
			i.Status = "interrupted"
		}
		return i, err
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) startImport(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Security string `json:"security"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	in.Host = strings.ToLower(strings.TrimSpace(in.Host))
	if in.Port == 0 {
		in.Port = 993
	}
	if !domainPattern.MatchString(in.Host) || in.Port < 1 || in.Port > 65535 || (in.Security != "tls" && in.Security != "starttls") {
		writeError(w, http.StatusUnprocessableEntity, "invalid_server", "Enter the other provider's IMAP server name, such as imap.gmail.com, and its port.")
		return
	}
	if strings.TrimSpace(in.Username) == "" || in.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_credentials", "Enter the username and password for the other account.")
		return
	}
	if _, live := s.importForMailbox(m.ID); live {
		writeError(w, http.StatusConflict, "import_running", "An import into this mailbox is already running. Wait for it to finish or cancel it.")
		return
	}
	dialer := publicDialer(s.cfg.WebhookAllowPrivate)
	src := mail.ImportSource{Host: in.Host, Port: in.Port, StartTLS: in.Security == "starttls", Username: strings.TrimSpace(in.Username), Password: in.Password, Dial: dialer.DialContext}
	checkCtx, cancelCheck := context.WithTimeout(r.Context(), 30*time.Second)
	err := mail.CheckImportSource(checkCtx, src)
	cancelCheck()
	if err != nil {
		message := "Aether could not connect to " + in.Host + ". Check the server name, port and security, and that the provider allows IMAP access."
		if errors.Is(err, mail.ErrImportLogin) {
			message = "The other provider rejected the username or password. Many providers, such as Gmail, iCloud and Yahoo, need an app password for IMAP."
		}
		writeError(w, http.StatusUnprocessableEntity, "import_connect_failed", message)
		return
	}

	var job mailImport
	err = s.db.QueryRow(r.Context(), `INSERT INTO mail_imports (mailbox_id, user_id, host, username) VALUES ($1, $2, $3, $4) RETURNING id, host, username, status, error, created_at`,
		m.ID, p.User.ID, in.Host, src.Username).Scan(&job.ID, &job.Host, &job.Username, &job.Status, &job.Error, &job.CreatedAt)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	s.imports.Store(job.ID, importRun{mailboxID: m.ID, cancel: cancel})
	go s.runImport(ctx, cancel, job.ID, m.Address, src)
	s.audit(r.Context(), r, p, &m.OrgID, "mail_import.start", "mailbox", m.ID, map[string]any{"host": in.Host})
	writeJSON(w, http.StatusAccepted, job)
}

type importRun struct {
	mailboxID string
	cancel    context.CancelFunc
}

func (s *Server) importForMailbox(mailboxID string) (string, bool) {
	var found string
	s.imports.Range(func(k, v any) bool {
		if v.(importRun).mailboxID == mailboxID {
			found = k.(string)
			return false
		}
		return true
	})
	return found, found != ""
}

func (s *Server) runImport(ctx context.Context, cancel context.CancelFunc, id, address string, src mail.ImportSource) {
	defer cancel()
	defer s.imports.Delete(id)
	var mu sync.Mutex
	last := time.Time{}
	save := func(p mail.ImportProgress, force bool) {
		mu.Lock()
		defer mu.Unlock()
		if !force && time.Since(last) < 2*time.Second {
			return
		}
		last = time.Now()
		body, _ := json.Marshal(p)
		dbCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		s.db.Exec(dbCtx, `UPDATE mail_imports SET progress = $2 WHERE id = $1`, id, body)
	}
	var latest mail.ImportProgress
	err := s.mail.Import(ctx, address, src, func(p mail.ImportProgress) {
		latest = p
		save(p, false)
	})
	save(latest, true)
	status, message := "done", ""
	switch {
	case errors.Is(err, context.Canceled):
		status = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		status, message = "failed", "The import stopped after 24 hours. Start it again to copy the rest; messages already copied are skipped."
	case errors.Is(err, mail.ErrImportLogin):
		status, message = "failed", "The other provider rejected the username or password."
	case err != nil:
		status, message = "failed", importFailure(err)
		s.log.Warn("mail import failed", "import", id, "err", err)
	}
	dbCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	s.db.Exec(dbCtx, `UPDATE mail_imports SET status = $2, error = $3, finished_at = now() WHERE id = $1`, id, status, message)
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
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	v, live := s.imports.Load(id)
	if !live || v.(importRun).mailboxID != m.ID {
		notFound(w)
		return
	}
	v.(importRun).cancel()
	w.WriteHeader(http.StatusNoContent)
}

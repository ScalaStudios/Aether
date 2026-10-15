package api

import (
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/scalastudios/aether/internal/auth"
	"github.com/scalastudios/aether/internal/secrets"
)

var localPartPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._+-]{0,62}[a-z0-9])?$`)

type mailbox struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"orgId"`
	DomainID    string    `json:"domainId"`
	Address     string    `json:"address"`
	LocalPart   string    `json:"localPart"`
	DisplayName string    `json:"displayName"`
	OwnerUserID *string   `json:"ownerUserId"`
	QuotaBytes  int64     `json:"quotaBytes"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"createdAt"`
}

const mailboxColumns = `id, org_id, domain_id, address, local_part, display_name, owner_user_id, quota_bytes, active, created_at`

func collectMailboxes(rows pgx.Rows, err error) ([]mailbox, error) {
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mailbox])
	return nonNil(out), err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Server) listMailboxes(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	out, err := collectMailboxes(s.db.Query(r.Context(), `SELECT `+mailboxColumns+` FROM mailboxes WHERE org_id = $1 ORDER BY address`, orgID))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) myMailboxes(w http.ResponseWriter, r *http.Request, p *principal) {
	out, err := collectMailboxes(s.db.Query(r.Context(), `SELECT `+mailboxColumns+` FROM mailboxes m WHERE m.active AND `+mailboxAccess("$1")+` ORDER BY m.owner_user_id = $1 DESC, address`, p.User.ID))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) orgDomain(r *http.Request, orgID, domainID string) (string, error) {
	var name string
	err := s.db.QueryRow(r.Context(), `SELECT name FROM domains WHERE id = $1 AND org_id = $2`, domainID, orgID).Scan(&name)
	return name, err
}

func (s *Server) createMailbox(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		DomainID    string `json:"domainId"`
		LocalPart   string `json:"localPart"`
		DisplayName string `json:"displayName"`
		QuotaBytes  int64  `json:"quotaBytes"`
		Password    string `json:"password"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	in.LocalPart = strings.ToLower(strings.TrimSpace(in.LocalPart))
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	switch {
	case !localPartPattern.MatchString(in.LocalPart):
		writeError(w, http.StatusUnprocessableEntity, "invalid_local_part", "Use letters, digits, dots, hyphens, underscores, or plus signs for the address.")
		return
	case in.QuotaBytes < 0:
		writeError(w, http.StatusUnprocessableEntity, "invalid_quota", "The quota cannot be negative.")
		return
	case in.Password != "" && len(in.Password) < 12:
		writeError(w, http.StatusUnprocessableEntity, "weak_password", "Use a password of at least 12 characters.")
		return
	}
	domainName, err := s.orgDomain(r, orgID, in.DomainID)
	if errors.Is(err, pgx.ErrNoRows) || !uuidPattern.MatchString(in.DomainID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_domain", "Choose a domain that belongs to this organization.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	address := in.LocalPart + "@" + domainName
	var m mailbox
	var code string
	err = pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		var aliasTaken bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM aliases WHERE address = $1)`, address).Scan(&aliasTaken); err != nil {
			return err
		}
		if aliasTaken {
			code = "address_taken"
			return nil
		}
		var userID string
		var hasPassword, isAdmin bool
		err := tx.QueryRow(r.Context(), `SELECT id, password_hash IS NOT NULL, is_platform_admin FROM users WHERE email = $1`, address).Scan(&userID, &hasPassword, &isAdmin)
		if errors.Is(err, pgx.ErrNoRows) {
			var hash *string
			if in.Password != "" {
				h := auth.HashPassword(in.Password)
				hash = &h
			}
			err = tx.QueryRow(r.Context(), `INSERT INTO users (email, display_name, password_hash) VALUES ($1, $2, $3) RETURNING id`, address, in.DisplayName, hash).Scan(&userID)
		} else if err == nil && in.Password != "" {
			if (hasPassword || isAdmin) && !p.User.IsPlatformAdmin {
				code = "account_exists"
				return nil
			}
			_, err = tx.Exec(r.Context(), `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, auth.HashPassword(in.Password))
		}
		if err != nil {
			return err
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO mailboxes (org_id, domain_id, local_part, address, display_name, owner_user_id, quota_bytes) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+mailboxColumns,
			orgID, in.DomainID, in.LocalPart, address, in.DisplayName, userID, in.QuotaBytes).
			Scan(&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT DO NOTHING`, orgID, userID)
		return err
	})
	switch {
	case code == "account_exists":
		writeError(w, http.StatusConflict, code, "An account with this address already has a password. Create the mailbox without a password; the account keeps its current sign-in.")
		return
	case code == "address_taken" || isUniqueViolation(err):
		writeError(w, http.StatusConflict, "address_taken", address+" is already in use by a mailbox or alias.")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "mailbox.create", "mailbox", m.ID, map[string]any{"address": address})
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) mailboxForAdmin(w http.ResponseWriter, r *http.Request, p *principal) (*mailbox, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, false
	}
	var m mailbox
	err := s.db.QueryRow(r.Context(), `SELECT `+mailboxColumns+` FROM mailboxes WHERE id = $1`, id).
		Scan(&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.orgRole(m.OrgID) == "") {
		notFound(w)
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	if !p.canAdminOrg(m.OrgID) {
		forbidden(w)
		return nil, false
	}
	return &m, true
}

func (s *Server) updateMailbox(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	var in struct {
		DisplayName *string `json:"displayName"`
		QuotaBytes  *int64  `json:"quotaBytes"`
		Active      *bool   `json:"active"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	if in.QuotaBytes != nil && *in.QuotaBytes < 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_quota", "The quota cannot be negative.")
		return
	}
	err := s.db.QueryRow(r.Context(), `
		UPDATE mailboxes SET display_name = coalesce($2, display_name), quota_bytes = coalesce($3, quota_bytes), active = coalesce($4, active)
		WHERE id = $1 RETURNING `+mailboxColumns, m.ID, in.DisplayName, in.QuotaBytes, in.Active).
		Scan(&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.update", "mailbox", m.ID, map[string]any{"address": m.Address, "active": m.Active, "quotaBytes": m.QuotaBytes})
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) deleteMailbox(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM mailboxes WHERE id = $1`, m.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.delete", "mailbox", m.ID, map[string]any{"address": m.Address})
	w.WriteHeader(http.StatusNoContent)
}

type alias struct {
	ID           string   `json:"id"`
	OrgID        string   `json:"orgId"`
	DomainID     string   `json:"domainId"`
	Address      string   `json:"address"`
	LocalPart    string   `json:"localPart"`
	Destinations []string `json:"destinations"`
	Active       bool     `json:"active"`
}

func (s *Server) listAliases(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, org_id, domain_id, address, local_part, destinations, active FROM aliases WHERE org_id = $1 ORDER BY address`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[alias])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) createAlias(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		DomainID     string   `json:"domainId"`
		LocalPart    string   `json:"localPart"`
		Destinations []string `json:"destinations"`
	}
	if !decode(w, r, 32<<10, &in) {
		return
	}
	in.LocalPart = strings.ToLower(strings.TrimSpace(in.LocalPart))
	if in.LocalPart != "" && !localPartPattern.MatchString(in.LocalPart) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_local_part", "Use letters, digits, dots, hyphens, underscores, or plus signs, or leave it empty for a catch-all.")
		return
	}
	if len(in.Destinations) == 0 || len(in.Destinations) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_destinations", "Add between 1 and 100 destination addresses.")
		return
	}
	for i, d := range in.Destinations {
		a, err := mail.ParseAddress(d)
		if err != nil || a.Name != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_destinations", d+" is not a valid email address.")
			return
		}
		in.Destinations[i] = strings.ToLower(a.Address)
	}
	domainName, err := s.orgDomain(r, orgID, in.DomainID)
	if errors.Is(err, pgx.ErrNoRows) || !uuidPattern.MatchString(in.DomainID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_domain", "Choose a domain that belongs to this organization.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	address := in.LocalPart + "@" + domainName
	var exists bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE address = $1)`, address).Scan(&exists); err != nil {
		s.internalError(w, r, err)
		return
	}
	if exists {
		writeError(w, http.StatusConflict, "address_taken", address+" is already a mailbox.")
		return
	}
	var a alias
	err = s.db.QueryRow(r.Context(), `INSERT INTO aliases (org_id, domain_id, local_part, address, destinations) VALUES ($1, $2, $3, $4, $5) RETURNING id, org_id, domain_id, address, local_part, destinations, active`,
		orgID, in.DomainID, in.LocalPart, address, in.Destinations).Scan(&a.ID, &a.OrgID, &a.DomainID, &a.Address, &a.LocalPart, &a.Destinations, &a.Active)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "address_taken", address+" is already an alias.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "alias.create", "alias", a.ID, map[string]any{"address": address, "destinations": a.Destinations})
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "aliasId")
	if !ok {
		return
	}
	var orgID, address string
	err := s.db.QueryRow(r.Context(), `SELECT org_id, address FROM aliases WHERE id = $1`, id).Scan(&orgID, &address)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.orgRole(orgID) == "") {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !p.canAdminOrg(orgID) {
		forbidden(w)
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM aliases WHERE id = $1`, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "alias.delete", "alias", id, map[string]any{"address": address})
	w.WriteHeader(http.StatusNoContent)
}

type appPassword struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	MailboxID      string    `json:"mailboxId"`
	MailboxAddress string    `json:"mailboxAddress"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (s *Server) listAppPasswords(w http.ResponseWriter, r *http.Request, p *principal) {
	rows, err := s.db.Query(r.Context(), `SELECT a.id, a.name, a.mailbox_id, m.address, a.created_at FROM app_passwords a JOIN mailboxes m ON m.id = a.mailbox_id WHERE a.user_id = $1 AND a.revoked_at IS NULL ORDER BY a.created_at DESC`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[appPassword])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) createAppPassword(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Name      string `json:"name"`
		MailboxID string `json:"mailboxId"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_name", "Name the app password after the device or app that will use it.")
		return
	}
	var out struct {
		appPassword
		Password string `json:"password"`
	}
	var orgID string
	out.Password = secrets.Token(20)
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO app_passwords (user_id, mailbox_id, name, token_hash)
		SELECT $1, m.id, $3, $4 FROM mailboxes m WHERE m.id = $2 AND m.active AND `+mailboxAccess("$1")+`
		RETURNING id, name, mailbox_id, created_at, (SELECT address FROM mailboxes WHERE id = $2), (SELECT org_id FROM mailboxes WHERE id = $2)`,
		p.User.ID, in.MailboxID, in.Name, secrets.SHA256Hex(out.Password)).
		Scan(&out.ID, &out.Name, &out.MailboxID, &out.CreatedAt, &out.MailboxAddress, &orgID)
	if errors.Is(err, pgx.ErrNoRows) || (err != nil && !uuidPattern.MatchString(in.MailboxID)) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_mailbox", "Choose one of your own active mailboxes.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "app_password.create", "app_password", out.ID, map[string]any{"mailbox": out.MailboxAddress})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) revokeAppPassword(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var orgID string
	err := s.db.QueryRow(r.Context(), `UPDATE app_passwords a SET revoked_at = now() FROM mailboxes m WHERE a.id = $1 AND a.user_id = $2 AND a.revoked_at IS NULL AND m.id = a.mailbox_id RETURNING m.org_id`, id, p.User.ID).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "app_password.revoke", "app_password", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"errors"
	"net/http"
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
	Kind        string    `json:"kind"`
}

const mailboxColumns = `id, org_id, domain_id, address, local_part, display_name, owner_user_id, quota_bytes, active, created_at, kind`

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
	rows, err := s.db.Query(r.Context(), `SELECT `+mailboxColumns+`, `+mailboxAccessLevel("$1")+`, `+mailboxSendAs("$1")+`, `+mailboxSendOnBehalf("$1")+` FROM mailboxes m WHERE m.active AND `+mailboxAccess("$1")+` ORDER BY coalesce(m.owner_user_id = $1, false) DESC, address`, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
		mailbox
		Access       string `json:"access"`
		SendAs       bool   `json:"sendAs"`
		SendOnBehalf bool   `json:"sendOnBehalf"`
	}])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
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
		Kind        string `json:"kind"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	in.LocalPart = strings.ToLower(strings.TrimSpace(in.LocalPart))
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.Kind == "" {
		in.Kind = "user"
	}
	switch {
	case in.Kind != "user" && in.Kind != "shared":
		writeError(w, http.StatusUnprocessableEntity, "invalid_kind", "Kind must be user or shared.")
		return
	case in.Kind == "shared" && in.Password != "":
		writeError(w, http.StatusUnprocessableEntity, "shared_no_password", "A shared mailbox has no sign-in. Leave the password empty and add members instead.")
		return
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
		if in.Kind == "shared" {
			if err == nil {
				code = "address_is_account"
				return nil
			}
			if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
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
		var owner *string
		if in.Kind == "user" {
			owner = &userID
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO mailboxes (org_id, domain_id, local_part, address, display_name, owner_user_id, quota_bytes, kind) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+mailboxColumns,
			orgID, in.DomainID, in.LocalPart, address, in.DisplayName, owner, in.QuotaBytes, in.Kind).
			Scan(&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt, &m.Kind)
		if err != nil || owner == nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT DO NOTHING`, orgID, userID)
		return err
	})
	switch {
	case code == "account_exists":
		writeError(w, http.StatusConflict, code, "An account with this address already has a password. Create the mailbox without a password; the account keeps its current sign-in.")
		return
	case code == "address_is_account":
		writeError(w, http.StatusConflict, code, "An account already signs in with this address. Convert that person's mailbox to shared instead, or choose another address.")
		return
	case code == "address_taken" || isUniqueViolation(err):
		writeError(w, http.StatusConflict, "address_taken", address+" is already in use by a mailbox or alias.")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "mailbox.create", "mailbox", m.ID, map[string]any{"address": address, "kind": m.Kind})
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) mailboxForAdmin(w http.ResponseWriter, r *http.Request, p *principal) (*mailbox, bool) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return nil, false
	}
	var m mailbox
	err := s.db.QueryRow(r.Context(), `SELECT `+mailboxColumns+` FROM mailboxes WHERE id = $1`, id).
		Scan(&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt, &m.Kind)
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
	if in.Active != nil && !*in.Active {
		if err := s.retireMailbox(r.Context(), m, false); err != nil {
			s.retireError(w, r, err)
			return
		}
		in.Active = nil
	}
	activate := in.Active != nil && *in.Active && m.Kind == "shared"
	if activate {
		if _, err := s.db.Exec(r.Context(), `UPDATE mailboxes SET `+markACLPending+` WHERE id = $1`, m.ID); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	err := s.db.QueryRow(r.Context(), `
		UPDATE mailboxes SET display_name = coalesce($2, display_name), quota_bytes = coalesce($3, quota_bytes), active = coalesce($4, active)
		WHERE id = $1 RETURNING `+mailboxColumns, m.ID, in.DisplayName, in.QuotaBytes, in.Active).
		Scan(&m.ID, &m.OrgID, &m.DomainID, &m.Address, &m.LocalPart, &m.DisplayName, &m.OwnerUserID, &m.QuotaBytes, &m.Active, &m.CreatedAt, &m.Kind)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.update", "mailbox", m.ID, map[string]any{"address": m.Address, "active": m.Active, "quotaBytes": m.QuotaBytes})
	if activate {
		if err := s.syncMailboxACL(r.Context(), m.ID); err != nil {
			s.log.Warn("mailbox ACL sync failed; retrying", "mailbox", m.ID, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) deleteMailbox(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	if err := s.retireMailbox(r.Context(), m, true); err != nil {
		s.retireError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.delete", "mailbox", m.ID, map[string]any{"address": m.Address})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) convertMailbox(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.mailboxForAdmin(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Kind string `json:"kind"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	switch {
	case in.Kind != "user" && in.Kind != "shared":
		writeError(w, http.StatusUnprocessableEntity, "invalid_kind", "Kind must be user or shared.")
		return
	case in.Kind == m.Kind:
		writeError(w, http.StatusUnprocessableEntity, "same_kind", "This is already a "+m.Kind+" mailbox.")
		return
	}
	meta := map[string]any{"address": m.Address, "from": m.Kind, "to": in.Kind, "accountDeprovisioned": false, "accountRestored": false}
	var refused bool
	var ownerMailboxes []string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		if in.Kind == "shared" {
			var ownerEmail string
			var ownerIsAdmin bool
			if m.OwnerUserID != nil {
				if err := tx.QueryRow(r.Context(), `SELECT email, is_platform_admin FROM users WHERE id = $1`, *m.OwnerUserID).Scan(&ownerEmail, &ownerIsAdmin); err != nil {
					return err
				}
				meta["formerOwner"] = ownerEmail
			}
			deprovision := ownerEmail == m.Address
			if deprovision && ownerIsAdmin {
				refused = true
				return nil
			}
			if _, err := tx.Exec(r.Context(), `UPDATE mailboxes SET kind = 'shared', owner_user_id = NULL, `+markACLPending+` WHERE id = $1`, m.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `UPDATE app_passwords SET revoked_at = now() WHERE mailbox_id = $1 AND revoked_at IS NULL`, m.ID); err != nil {
				return err
			}
			if err := queueKicks(r.Context(), tx, []string{m.Address}); err != nil {
				return err
			}
			if m.OwnerUserID == nil {
				return nil
			}
			if deprovision {
				meta["accountDeprovisioned"] = true
				if _, err := tx.Exec(r.Context(), `UPDATE users SET status = 'deprovisioned', suspended_by = 'mailbox-conversion', updated_at = now() WHERE id = $1`, *m.OwnerUserID); err != nil {
					return err
				}
				if _, err := tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1`, *m.OwnerUserID); err != nil {
					return err
				}
				if _, err := tx.Exec(r.Context(), `UPDATE app_passwords SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, *m.OwnerUserID); err != nil {
					return err
				}
			}
			ids, err := userMailboxIDs(r.Context(), tx, *m.OwnerUserID)
			if err != nil {
				return err
			}
			ownerMailboxes = ids
			return s.revokeMailAccess(r.Context(), tx, *m.OwnerUserID, ids)
		}
		if _, err := tx.Exec(r.Context(), `UPDATE mailboxes SET kind = 'user', `+markACLPending+` WHERE id = $1`, m.ID); err != nil {
			return err
		}
		var ownerID string
		err := tx.QueryRow(r.Context(), `
			UPDATE users SET status = 'active', suspended_by = '', updated_at = now()
			WHERE email = $1 AND status = 'deprovisioned' AND suspended_by = 'mailbox-conversion' RETURNING id`, m.Address).Scan(&ownerID)
		if err == nil {
			meta["accountRestored"] = true
			m.OwnerUserID = &ownerID
			if _, err := tx.Exec(r.Context(), `UPDATE mailboxes SET owner_user_id = $2 WHERE id = $1`, m.ID, ownerID); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `DELETE FROM mailbox_delegates WHERE mailbox_id = $1 AND user_id = $2`, m.ID, ownerID); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT DO NOTHING`, m.OrgID, ownerID); err != nil {
				return err
			}
			ids, err := userMailboxIDs(r.Context(), tx, ownerID)
			if err != nil {
				return err
			}
			ownerMailboxes = ids
			if err := s.revokeMailAccess(r.Context(), tx, ownerID, ids); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT u.email FROM mailbox_delegates md JOIN users u ON u.id = md.user_id WHERE md.mailbox_id = $1`, m.ID)
		if err != nil {
			return err
		}
		members, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		return queueKicks(r.Context(), tx, members)
	})
	if refused {
		writeError(w, http.StatusUnprocessableEntity, "owner_is_admin", "The owner is a platform administrator. Converting this mailbox would disable their account.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	m.Kind = in.Kind
	if m.Kind == "shared" {
		m.OwnerUserID = nil
	}
	s.audit(r.Context(), r, p, &m.OrgID, "mailbox.convert", "mailbox", m.ID, meta)
	for _, id := range append([]string{m.ID}, ownerMailboxes...) {
		if err := s.syncMailboxACL(r.Context(), id); err != nil {
			s.log.Warn("mailbox ACL sync failed; retrying", "mailbox", id, "err", err)
		}
	}
	wake(s.kickWake)
	writeJSON(w, http.StatusOK, m)
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

type createdAppPassword struct {
	appPassword
	Password string `json:"password"`
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
	if out, ok := s.issueAppPassword(w, r, p, in.MailboxID, in.Name); ok {
		writeJSON(w, http.StatusCreated, out)
	}
}

func (s *Server) issueAppPassword(w http.ResponseWriter, r *http.Request, p *principal, mailboxID, name string) (*createdAppPassword, bool) {
	var out createdAppPassword
	var orgID string
	out.Password = secrets.Token(20)
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO app_passwords (user_id, mailbox_id, name, token_hash)
		SELECT $1, m.id, $3, $4 FROM mailboxes m WHERE m.id = $2 AND m.active AND `+signInAsMailbox("$1")+`
		RETURNING id, name, mailbox_id, created_at, (SELECT address FROM mailboxes WHERE id = $2), (SELECT org_id FROM mailboxes WHERE id = $2)`,
		p.User.ID, mailboxID, name, secrets.SHA256Hex(out.Password)).
		Scan(&out.ID, &out.Name, &out.MailboxID, &out.CreatedAt, &out.MailboxAddress, &orgID)
	if errors.Is(err, pgx.ErrNoRows) || (err != nil && !uuidPattern.MatchString(mailboxID)) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_mailbox", "Choose one of your own active mailboxes.")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	s.audit(r.Context(), r, p, &orgID, "app_password.create", "app_password", out.ID, map[string]any{"mailbox": out.MailboxAddress})
	return &out, true
}

func (s *Server) revokeAppPassword(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var orgID string
	err := pgx.BeginFunc(r.Context(), s.db, func(tx pgx.Tx) error {
		var address string
		if err := tx.QueryRow(r.Context(), `UPDATE app_passwords a SET revoked_at = now() FROM mailboxes m WHERE a.id = $1 AND a.user_id = $2 AND a.revoked_at IS NULL AND m.id = a.mailbox_id RETURNING m.org_id, m.address`, id, p.User.ID).Scan(&orgID, &address); err != nil {
			return err
		}
		return queueKicks(r.Context(), tx, []string{address})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "app_password.revoke", "app_password", id, nil)
	wake(s.kickWake)
	w.WriteHeader(http.StatusNoContent)
}

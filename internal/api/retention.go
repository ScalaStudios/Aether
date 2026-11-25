package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const maxRetentionDays = 36500

var builtInRetention = map[string]int{"Junk": 30, "Trash": 30}

type retentionPolicy struct {
	ID        string    `json:"id"`
	MailboxID *string   `json:"mailboxId"`
	Address   *string   `json:"mailboxAddress"`
	Action    string    `json:"action"`
	Folder    string    `json:"folder"`
	Days      *int      `json:"days"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type retentionRule struct {
	Folder string `json:"folder"`
	Days   int    `json:"days"`
}

type mailboxRetention struct {
	KeepDays int             `json:"keepDays"`
	Folders  []retentionRule `json:"folders"`
}

type retentionRow struct {
	Mailbox bool
	Action  string
	Folder  string
	Days    *int
}

func retentionFolder(folder string) (string, bool) {
	folder = strings.TrimSpace(folder)
	if strings.EqualFold(folder, "INBOX") {
		return "INBOX", true
	}
	ok := folder != "" && utf8.RuneCountInString(folder) <= 200 && !strings.ContainsAny(folder, "*%") && !strings.ContainsFunc(folder, unicode.IsControl) && !strings.HasPrefix(folder, "/") && !strings.HasSuffix(folder, "/") && !mailpkg.SharedFolder(folder)
	return folder, ok
}

func retentionDaysProblem(action string, days *int) string {
	if days == nil && action == "keep" {
		return "Enter how many days to keep messages."
	}
	if days != nil && (*days < 1 || *days > maxRetentionDays) {
		return "Enter a number of days between 1 and 36,500."
	}
	return ""
}

func resolveRetention(rows []retentionRow) mailboxRetention {
	slices.SortStableFunc(rows, func(a, b retentionRow) int {
		if a.Mailbox == b.Mailbox {
			return 0
		}
		if a.Mailbox {
			return 1
		}
		return -1
	})
	deleteAfter := map[string]*int{}
	for folder, days := range builtInRetention {
		deleteAfter[folder] = &days
	}
	out := mailboxRetention{Folders: []retentionRule{}}
	for _, r := range rows {
		switch {
		case r.Action == "keep" && r.Days != nil:
			out.KeepDays = max(out.KeepDays, *r.Days)
		case r.Action == "delete":
			deleteAfter[r.Folder] = r.Days
		}
	}
	for folder, days := range deleteAfter {
		if days != nil {
			out.Folders = append(out.Folders, retentionRule{Folder: folder, Days: *days})
		}
	}
	slices.SortFunc(out.Folders, func(a, b retentionRule) int { return cmp.Compare(a.Folder, b.Folder) })
	return out
}

func expungeQuery(folder string, days, keep int) []string {
	if folder == "Trash" || folder == "Junk" {
		query := []string{"mailbox", folder, "savedbefore", strconv.Itoa(days) + "d"}
		if keep > 0 {
			query = append(query, "before", strconv.Itoa(keep)+"d")
		}
		return query
	}
	return []string{"mailbox", folder, "before", strconv.Itoa(max(days, keep)) + "d"}
}

func (s *Server) effectiveRetention(ctx context.Context, orgID, mailboxID string) (mailboxRetention, error) {
	rows, err := s.db.Query(ctx, `SELECT mailbox_id IS NOT NULL, action, folder, days FROM retention_policies WHERE org_id = $1 AND (mailbox_id IS NULL OR mailbox_id = $2)`, orgID, mailboxID)
	if err != nil {
		return mailboxRetention{}, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[retentionRow])
	if err != nil {
		return mailboxRetention{}, err
	}
	return resolveRetention(list), nil
}

func (s *Server) getMailboxRetention(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	out, err := s.effectiveRetention(r.Context(), m.OrgID, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

const retentionPolicyColumns = `p.id, p.mailbox_id, m.address, p.action, p.folder, p.days, p.updated_at FROM retention_policies p LEFT JOIN mailboxes m ON m.id = p.mailbox_id`

func (s *Server) listRetentionPolicies(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+retentionPolicyColumns+` WHERE p.org_id = $1 ORDER BY m.address NULLS FIRST, p.action DESC, p.folder`, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	policies, err := pgx.CollectRows(rows, pgx.RowToStructByPos[retentionPolicy])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	builtIn := []retentionRule{}
	for folder, days := range builtInRetention {
		builtIn = append(builtIn, retentionRule{Folder: folder, Days: days})
	}
	slices.SortFunc(builtIn, func(a, b retentionRule) int { return cmp.Compare(a.Folder, b.Folder) })
	writeJSON(w, http.StatusOK, map[string]any{"builtIn": builtIn, "policies": nonNil(policies)})
}

func (s *Server) createRetentionPolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		MailboxID *string `json:"mailboxId"`
		Action    string  `json:"action"`
		Folder    string  `json:"folder"`
		Days      *int    `json:"days"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	switch in.Action {
	case "keep":
		in.Folder = ""
	case "delete":
		var valid bool
		if in.Folder, valid = retentionFolder(in.Folder); !valid {
			writeError(w, http.StatusUnprocessableEntity, "invalid_folder", "Enter the folder’s name, up to 200 characters, without * or %. Shared folders cannot have a policy.")
			return
		}
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid_action", "Choose whether the policy deletes or keeps messages.")
		return
	}
	if msg := retentionDaysProblem(in.Action, in.Days); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_days", msg)
		return
	}
	out := retentionPolicy{Action: in.Action, Folder: in.Folder, Days: in.Days}
	if in.MailboxID != nil {
		id := strings.ToLower(*in.MailboxID)
		var address string
		err := pgx.ErrNoRows
		if uuidPattern.MatchString(id) {
			err = s.db.QueryRow(r.Context(), `SELECT address FROM mailboxes WHERE id = $1 AND org_id = $2`, id, orgID).Scan(&address)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_mailbox", "Choose one of this organization’s mailboxes.")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out.MailboxID, out.Address = &id, &address
	}
	err := s.db.QueryRow(r.Context(), `INSERT INTO retention_policies (org_id, mailbox_id, action, folder, days) VALUES ($1, $2, $3, $4, $5) RETURNING id, updated_at`,
		orgID, out.MailboxID, out.Action, out.Folder, out.Days).Scan(&out.ID, &out.UpdatedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "policy_exists", "A policy for this folder and scope already exists. Edit it instead.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.retentionChanged(r.Context(), orgID, out.MailboxID)
	s.audit(r.Context(), r, p, &orgID, "retention_policy.create", "retention_policy", out.ID, retentionAuditMeta(&out, nil))
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) retentionPolicyFromPath(w http.ResponseWriter, r *http.Request, p *principal) (string, *retentionPolicy, bool) {
	id, ok := pathID(w, r, "policyId")
	if !ok {
		return "", nil, false
	}
	var orgID string
	var out retentionPolicy
	err := s.db.QueryRow(r.Context(), `SELECT p.org_id, `+retentionPolicyColumns+` WHERE p.id = $1`, id).
		Scan(&orgID, &out.ID, &out.MailboxID, &out.Address, &out.Action, &out.Folder, &out.Days, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.orgRole(orgID) == "") {
		notFound(w)
		return "", nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return "", nil, false
	}
	if !p.canAdminOrg(orgID) {
		forbidden(w)
		return "", nil, false
	}
	return orgID, &out, true
}

func (s *Server) updateRetentionPolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, policy, ok := s.retentionPolicyFromPath(w, r, p)
	if !ok {
		return
	}
	var in struct {
		Days *int `json:"days"`
	}
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if msg := retentionDaysProblem(policy.Action, in.Days); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_days", msg)
		return
	}
	previous := policy.Days
	if err := s.db.QueryRow(r.Context(), `UPDATE retention_policies SET days = $2, updated_at = now() WHERE id = $1 RETURNING days, updated_at`, policy.ID, in.Days).Scan(&policy.Days, &policy.UpdatedAt); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.retentionChanged(r.Context(), orgID, policy.MailboxID)
	s.audit(r.Context(), r, p, &orgID, "retention_policy.update", "retention_policy", policy.ID, retentionAuditMeta(policy, map[string]any{"previousDays": previous}))
	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) deleteRetentionPolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, policy, ok := s.retentionPolicyFromPath(w, r, p)
	if !ok {
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM retention_policies WHERE id = $1`, policy.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.retentionChanged(r.Context(), orgID, policy.MailboxID)
	s.audit(r.Context(), r, p, &orgID, "retention_policy.remove", "retention_policy", policy.ID, retentionAuditMeta(policy, nil))
	w.WriteHeader(http.StatusNoContent)
}

func retentionAuditMeta(policy *retentionPolicy, extra map[string]any) map[string]any {
	meta := map[string]any{"mailboxId": policy.MailboxID, "mailbox": policy.Address, "action": policy.Action, "folder": policy.Folder, "days": policy.Days}
	for k, v := range extra {
		meta[k] = v
	}
	return meta
}

func (s *Server) retentionChanged(ctx context.Context, orgID string, mailboxID *string) {
	if _, err := s.db.Exec(ctx, `UPDATE retention_runs SET next_run_at = now() WHERE mailbox_id IN (SELECT id FROM mailboxes WHERE org_id = $1 AND ($2::uuid IS NULL OR id = $2))`, orgID, mailboxID); err != nil {
		s.log.Error("scheduling retention after a policy change failed", "org", orgID, "err", err)
	}
}

func (s *Server) RunRetention(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		for s.applyNextRetention(ctx) {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) applyNextRetention(ctx context.Context) bool {
	var id, orgID, address string
	var lease time.Time
	err := s.db.QueryRow(ctx, `
		WITH due AS (
			SELECT m.id, m.org_id, m.address FROM mailboxes m JOIN domains d ON d.id = m.domain_id LEFT JOIN retention_runs r ON r.mailbox_id = m.id
			WHERE m.active AND d.active AND d.verified_at IS NOT NULL
			  AND (r.mailbox_id IS NULL OR (r.next_run_at <= now() AND (r.lease_until IS NULL OR r.lease_until < now())))
			ORDER BY r.next_run_at NULLS FIRST LIMIT 1),
		claimed AS (
			INSERT INTO retention_runs (mailbox_id, lease_until) SELECT id, now() + interval '30 minutes' FROM due
			ON CONFLICT (mailbox_id) DO UPDATE SET lease_until = excluded.lease_until
			WHERE retention_runs.next_run_at <= now() AND (retention_runs.lease_until IS NULL OR retention_runs.lease_until < now())
			RETURNING mailbox_id, lease_until)
		SELECT due.id, due.org_id, due.address, claimed.lease_until FROM due JOIN claimed ON claimed.mailbox_id = due.id`).Scan(&id, &orgID, &address, &lease)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
			s.log.Error("claiming a mailbox for retention failed", "err", err)
		}
		return false
	}
	policy, err := s.effectiveRetention(ctx, orgID, id)
	hold, holdErr := s.holdScope(ctx, id)
	err = errors.Join(err, holdErr)
	for _, rule := range policy.Folders {
		if err != nil {
			break
		}
		err = s.mail.ExpungeHeld(ctx, address, expungeQuery(rule.Folder, rule.Days, policy.KeepDays), hold)
	}
	next := "1 day"
	if err != nil {
		mailBackendErrs.Inc()
		s.log.Warn("applying retention failed; retrying in an hour", "mailbox", id, "err", err)
		next = "1 hour"
	}
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	if _, err := s.db.Exec(ctx, `UPDATE retention_runs SET lease_until = NULL, next_run_at = now() + $3::interval WHERE mailbox_id = $1 AND lease_until = $2`, id, lease, next); err != nil {
		s.log.Error("releasing the retention lease failed", "mailbox", id, "err", err)
		return false
	}
	return true
}

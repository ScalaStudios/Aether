package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const quarantinePage = 50

var errAlreadyReleased = errors.New("the message was already released")

type quarantineItem struct {
	ID          string          `json:"id"`
	MailboxID   string          `json:"mailboxId"`
	Mailbox     string          `json:"mailbox"`
	Recipient   string          `json:"recipient"`
	Category    string          `json:"category"`
	Access      string          `json:"access"`
	Status      string          `json:"status"`
	From        mailpkg.Address `json:"from"`
	Subject     string          `json:"subject"`
	Score       float64         `json:"score"`
	Size        int             `json:"size"`
	ReceivedAt  time.Time       `json:"receivedAt"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	RequestedAt *time.Time      `json:"requestedAt"`
	ReleasedAt  *time.Time      `json:"releasedAt"`
	ReleasedBy  *string         `json:"releasedBy"`
	orgID       string
}

type quarantinePreview struct {
	quarantineItem
	Message        *mailpkg.Message   `json:"message"`
	Symbols        []quarantineSymbol `json:"symbols"`
	CanAllowSender bool               `json:"canAllowSender"`
}

type quarantinePolicy struct {
	Access        map[string]string `json:"access"`
	RetentionDays int               `json:"retentionDays"`
	Digest        string            `json:"digest"`
}

func quarantineSelect(extra string) string {
	return `SELECT q.id, q.mailbox_id, m.address, q.recipient, qm.category, ` + quarantineAccess("qm.category") + `, q.status,
		qm.from_address, qm.from_name, qm.subject, qm.score, qm.size, qm.received_at,
		q.created_at + make_interval(days => coalesce(p.retention_days, 30)), q.requested_at, q.released_at, ru.email, q.org_id` + extra + `
		FROM quarantine_items q JOIN quarantine_messages qm ON qm.id = q.message_id JOIN mailboxes m ON m.id = q.mailbox_id
		LEFT JOIN quarantine_policies p ON p.org_id = q.org_id LEFT JOIN users ru ON ru.id = q.released_by`
}

func (it *quarantineItem) dest(extra ...any) []any {
	return append([]any{&it.ID, &it.MailboxID, &it.Mailbox, &it.Recipient, &it.Category, &it.Access, &it.Status,
		&it.From.Address, &it.From.Name, &it.Subject, &it.Score, &it.Size, &it.ReceivedAt,
		&it.ExpiresAt, &it.RequestedAt, &it.ReleasedAt, &it.ReleasedBy, &it.orgID}, extra...)
}

func (s *Server) listQuarantine(ctx context.Context, where []string, args []any, before string) ([]quarantineItem, *string, error) {
	if before != "" {
		args = append(args, before)
		where = append(where, fmt.Sprintf(`(q.created_at, q.id) < (SELECT created_at, id FROM quarantine_items WHERE id = $%d)`, len(args)))
	}
	rows, err := s.db.Query(ctx, quarantineSelect("")+` WHERE `+strings.Join(where, " AND ")+fmt.Sprintf(` ORDER BY q.created_at DESC, q.id DESC LIMIT %d`, quarantinePage+1), args...)
	if err != nil {
		return nil, nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (quarantineItem, error) {
		var it quarantineItem
		return it, row.Scan(it.dest()...)
	})
	if err != nil || len(items) <= quarantinePage {
		return nonNil(items), nil, err
	}
	items = items[:quarantinePage]
	return items, &items[quarantinePage-1].ID, nil
}

func (s *Server) quarantinePreview(ctx context.Context, where string, args ...any) (*quarantinePreview, error) {
	out := &quarantinePreview{}
	var raw []byte
	err := s.db.QueryRow(ctx, quarantineSelect(", qm.raw, qm.symbols")+` WHERE `+where, args...).Scan(out.dest(&raw, &out.Symbols)...)
	if err != nil {
		return nil, err
	}
	if out.Message, err = mailpkg.ParseRaw(raw); err != nil {
		return nil, err
	}
	slices.SortStableFunc(out.Symbols, func(a, b quarantineSymbol) int { return cmp.Compare(b.Score, a.Score) })
	return out, nil
}

func (s *Server) mailboxQuarantineItem(w http.ResponseWriter, r *http.Request, p *principal) (*ownedMailbox, *quarantinePreview, bool) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return nil, nil, false
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return nil, nil, false
	}
	item, err := s.quarantinePreview(r.Context(), `q.id = $1 AND q.mailbox_id = $2 AND q.status <> 'released' AND `+quarantineAccess("qm.category")+` <> 'none'`, id, m.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return nil, nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	item.CanAllowSender = item.Access == "release" && item.From.Address != "" && (item.Category == "spam" || item.Category == "high_confidence_spam")
	return m, item, true
}

func (s *Server) releaseQuarantined(ctx context.Context, r *http.Request, p *principal, id string) error {
	var item quarantineItem
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `
			SELECT q.status, m.address, q.org_id, qm.from_address, qm.category, qm.raw
			FROM quarantine_items q JOIN quarantine_messages qm ON qm.id = q.message_id JOIN mailboxes m ON m.id = q.mailbox_id
			WHERE q.id = $1 FOR UPDATE OF q`, id).Scan(&item.Status, &item.Mailbox, &item.orgID, &item.From.Address, &item.Category, &raw)
		if err != nil {
			return err
		}
		if item.Status == "released" {
			return errAlreadyReleased
		}
		if err := s.mail.Deliver(item.Mailbox, raw); err != nil {
			return fmt.Errorf("%w: %w", errMailUnavailable, err)
		}
		_, err = tx.Exec(ctx, `UPDATE quarantine_items SET status = 'released', released_at = now(), released_by = $2 WHERE id = $1`, id, p.User.ID)
		return err
	})
	if err != nil {
		return err
	}
	s.audit(ctx, r, p, &item.orgID, "quarantine.release", "quarantine_item", id, map[string]any{"mailbox": item.Mailbox, "sender": item.From.Address, "category": item.Category})
	return nil
}

func (s *Server) quarantineActionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errAlreadyReleased):
		writeError(w, http.StatusConflict, "already_released", "Someone already released this message.")
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w)
	case errors.Is(err, errMailUnavailable):
		s.mailError(w, r, err)
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) deleteQuarantined(ctx context.Context, r *http.Request, p *principal, where string, args ...any) (int, error) {
	rows, err := s.db.Query(ctx, `
		WITH gone AS (DELETE FROM quarantine_items q WHERE `+where+` RETURNING q.id, q.message_id, q.mailbox_id, q.org_id)
		SELECT g.id, g.org_id, m.address, qm.from_address, qm.category FROM gone g JOIN mailboxes m ON m.id = g.mailbox_id JOIN quarantine_messages qm ON qm.id = g.message_id`, args...)
	if err != nil {
		return 0, err
	}
	gone, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (quarantineItem, error) {
		var it quarantineItem
		return it, row.Scan(&it.ID, &it.orgID, &it.Mailbox, &it.From.Address, &it.Category)
	})
	for _, it := range gone {
		s.audit(ctx, r, p, &it.orgID, "quarantine.delete", "quarantine_item", it.ID, map[string]any{"mailbox": it.Mailbox, "sender": it.From.Address, "category": it.Category})
	}
	return len(gone), err
}

func (s *Server) listMailboxQuarantine(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	where := []string{`q.mailbox_id = $1`, `q.status <> 'released'`, quarantineAccess("qm.category") + ` <> 'none'`}
	items, next, err := s.listQuarantine(r.Context(), where, []any{m.ID}, r.URL.Query().Get("before"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var total int
	var retention int
	err = s.db.QueryRow(r.Context(), `
		SELECT (SELECT count(*) FROM quarantine_items q JOIN quarantine_messages qm ON qm.id = q.message_id LEFT JOIN quarantine_policies p ON p.org_id = q.org_id WHERE `+strings.Join(where, " AND ")+`),
		       coalesce((SELECT retention_days FROM quarantine_policies WHERE org_id = $2), 30)`, m.ID, m.OrgID).Scan(&total, &retention)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "nextCursor": next, "retentionDays": retention})
}

func (s *Server) getMailboxQuarantine(w http.ResponseWriter, r *http.Request, p *principal) {
	if _, item, ok := s.mailboxQuarantineItem(w, r, p); ok {
		writeJSON(w, http.StatusOK, item)
	}
}

func (s *Server) releaseMailboxQuarantine(w http.ResponseWriter, r *http.Request, p *principal) {
	_, item, ok := s.mailboxQuarantineItem(w, r, p)
	if !ok {
		return
	}
	if item.Access != "release" {
		writeError(w, http.StatusForbidden, "release_not_allowed", "Your organization only lets you request the release of this message. An administrator decides.")
		return
	}
	if err := s.releaseQuarantined(r.Context(), r, p, item.ID); err != nil {
		s.quarantineActionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) allowQuarantineSender(w http.ResponseWriter, r *http.Request, p *principal) {
	m, item, ok := s.mailboxQuarantineItem(w, r, p)
	if !ok {
		return
	}
	if !item.CanAllowSender {
		writeError(w, http.StatusForbidden, "allow_not_allowed", "Senders can be allowed only from messages held as spam that you may release yourself.")
		return
	}
	if err := s.releaseQuarantined(r.Context(), r, p, item.ID); err != nil {
		s.quarantineActionError(w, r, err)
		return
	}
	sender := strings.ToLower(item.From.Address)
	if _, ok := s.changeMailboxSenderList(w, r, m, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO sender_list_entries (org_id, mailbox_id, action, kind, value, created_by) VALUES ($1, $2, 'allow', 'address', $3, $4)
			ON CONFLICT (org_id, domain_id, mailbox_id, value) DO UPDATE SET action = 'allow', created_by = excluded.created_by, created_at = now()`, m.OrgID, m.ID, sender, creator(p))
		return err
	}); !ok {
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "quarantine.allow_sender", "mailbox", m.ID, map[string]any{"mailbox": m.Address, "sender": sender})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requestQuarantineRelease(w http.ResponseWriter, r *http.Request, p *principal) {
	m, item, ok := s.mailboxQuarantineItem(w, r, p)
	if !ok {
		return
	}
	if item.Access != "request" {
		writeError(w, http.StatusUnprocessableEntity, "request_not_needed", "You can release this message yourself.")
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE quarantine_items SET status = 'requested', requested_at = now(), requested_by = $2 WHERE id = $1 AND status = 'held'`, item.ID, p.User.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if tag.RowsAffected() == 1 {
		s.audit(r.Context(), r, p, &m.OrgID, "quarantine.request_release", "quarantine_item", item.ID, map[string]any{"mailbox": m.Address, "sender": item.From.Address, "category": item.Category})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteMailboxQuarantine(w http.ResponseWriter, r *http.Request, p *principal) {
	m, item, ok := s.mailboxQuarantineItem(w, r, p)
	if !ok {
		return
	}
	if _, err := s.deleteQuarantined(r.Context(), r, p, `q.id = $1 AND q.mailbox_id = $2`, item.ID, m.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAllowedSenders(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT value, created_at FROM sender_list_entries WHERE mailbox_id = $1 AND action = 'allow' AND kind = 'address' ORDER BY value`, m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type allowedSender struct {
		Sender    string    `json:"sender"`
		CreatedAt time.Time `json:"createdAt"`
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[allowedSender])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(out))
}

func (s *Server) removeAllowedSender(w http.ResponseWriter, r *http.Request, p *principal) {
	m, ok := s.ownedMailbox(w, r, p)
	if !ok {
		return
	}
	sender := strings.ToLower(r.URL.Query().Get("sender"))
	if _, ok := s.changeMailboxSenderList(w, r, m, func(tx pgx.Tx) error {
		var id string
		return tx.QueryRow(r.Context(), `DELETE FROM sender_list_entries WHERE mailbox_id = $1 AND action = 'allow' AND kind = 'address' AND value = $2 RETURNING id`, m.ID, sender).Scan(&id)
	}); !ok {
		return
	}
	s.audit(r.Context(), r, p, &m.OrgID, "quarantine.allowed_sender.remove", "mailbox", m.ID, map[string]any{"mailbox": m.Address, "sender": sender})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) quarantineReviewer(w http.ResponseWriter, r *http.Request, p *principal) (string, bool) {
	orgID, ok := pathID(w, r, "orgId")
	if !ok {
		return "", false
	}
	switch role := p.orgRole(orgID); {
	case role == "owner" || role == "admin":
		return orgID, true
	case p.orgRole(orgID) == "":
		notFound(w)
	default:
		writeError(w, http.StatusForbidden, "forbidden", "Only owners and administrators of this organization can review its quarantine.")
	}
	return "", false
}

func (s *Server) listOrgQuarantine(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.quarantineReviewer(w, r, p)
	if !ok {
		return
	}
	q := r.URL.Query()
	where, args := []string{`q.org_id = $1`}, []any{orgID}
	switch q.Get("status") {
	case "", "held":
		where = append(where, `q.status <> 'released'`)
	case "requested", "released":
		args = append(args, q.Get("status"))
		where = append(where, fmt.Sprintf(`q.status = $%d`, len(args)))
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid_status", "Filter by held, requested or released.")
		return
	}
	if c := q.Get("category"); c != "" {
		if !slices.Contains(quarantineCategories, c) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_category", "Filter by spam, high_confidence_spam, phishing or malware.")
			return
		}
		args = append(args, c)
		where = append(where, fmt.Sprintf(`qm.category = $%d`, len(args)))
	}
	if search := strings.TrimSpace(q.Get("q")); search != "" {
		args = append(args, strings.ToLower(search))
		where = append(where, fmt.Sprintf(`strpos(lower(qm.from_address || ' ' || qm.from_name || ' ' || qm.subject || ' ' || m.address || ' ' || q.recipient), $%d) > 0`, len(args)))
	}
	items, next, err := s.listQuarantine(r.Context(), where, args, q.Get("before"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var requested int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM quarantine_items WHERE org_id = $1 AND status = 'requested'`, orgID).Scan(&requested); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next, "requested": requested})
}

func (s *Server) getOrgQuarantine(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.quarantineReviewer(w, r, p)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	item, err := s.quarantinePreview(r.Context(), `q.id = $1 AND q.org_id = $2`, id, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) quarantineSelection(w http.ResponseWriter, r *http.Request, p *principal) (string, []string, bool) {
	orgID, ok := s.quarantineReviewer(w, r, p)
	if !ok {
		return "", nil, false
	}
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, 16<<10, &in) {
		return "", nil, false
	}
	if len(in.IDs) == 0 || len(in.IDs) > 100 || slices.ContainsFunc(in.IDs, func(id string) bool { return !uuidPattern.MatchString(id) }) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_ids", "Choose between 1 and 100 messages.")
		return "", nil, false
	}
	return orgID, in.IDs, true
}

func (s *Server) releaseOrgQuarantine(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ids, ok := s.quarantineSelection(w, r, p)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id FROM quarantine_items WHERE id = ANY($1) AND org_id = $2 AND status <> 'released'`, ids, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pending, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	released, failed := 0, 0
	for _, id := range pending {
		if err := s.releaseQuarantined(r.Context(), r, p, id); err != nil && !errors.Is(err, errAlreadyReleased) {
			s.log.Warn("releasing a quarantined message failed", "item", id, "err", err)
			failed++
			continue
		}
		released++
	}
	writeJSON(w, http.StatusOK, map[string]int{"released": released, "failed": failed})
}

func (s *Server) deleteOrgQuarantine(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ids, ok := s.quarantineSelection(w, r, p)
	if !ok {
		return
	}
	deleted, err := s.deleteQuarantined(r.Context(), r, p, `q.id = ANY($1) AND q.org_id = $2`, ids, orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted})
}

func (s *Server) loadQuarantinePolicy(ctx context.Context, orgID string) (*quarantinePolicy, error) {
	out := &quarantinePolicy{Access: maps.Clone(defaultQuarantineAccess), RetentionDays: 30, Digest: "daily"}
	var stored map[string]string
	err := s.db.QueryRow(ctx, `SELECT access, retention_days, digest FROM quarantine_policies WHERE org_id = $1`, orgID).Scan(&stored, &out.RetentionDays, &out.Digest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	maps.Copy(out.Access, stored)
	return out, nil
}

func (s *Server) getQuarantinePolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	policy, err := s.loadQuarantinePolicy(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func validQuarantinePolicy(in *quarantinePolicy) string {
	if len(in.Access) != len(quarantineCategories) {
		return "Set who may release spam, high_confidence_spam, phishing and malware."
	}
	for category, access := range in.Access {
		if !slices.Contains(quarantineCategories, category) || !slices.Contains(quarantineAccessLevels, access) {
			return "Each category is release, request or none."
		}
	}
	if in.RetentionDays < 1 || in.RetentionDays > 90 {
		return "Keep quarantined messages for 1 to 90 days."
	}
	if !slices.Contains([]string{"off", "daily", "weekly"}, in.Digest) {
		return "Send digests daily, weekly or not at all."
	}
	return ""
}

func (s *Server) saveQuarantinePolicy(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in quarantinePolicy
	if !decode(w, r, 4<<10, &in) {
		return
	}
	if msg := validQuarantinePolicy(&in); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_policy", msg)
		return
	}
	before, err := s.loadQuarantinePolicy(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO quarantine_policies (org_id, access, retention_days, digest) VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id) DO UPDATE SET access = excluded.access, retention_days = excluded.retention_days, digest = excluded.digest, updated_at = now()`,
		orgID, in.Access, in.RetentionDays, in.Digest)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !maps.Equal(before.Access, in.Access) || before.RetentionDays != in.RetentionDays || before.Digest != in.Digest {
		s.audit(r.Context(), r, p, &orgID, "quarantine.policy.update", "organization", orgID, map[string]any{
			"before": before, "after": in,
		})
	}
	writeJSON(w, http.StatusOK, in)
}

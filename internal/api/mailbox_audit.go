package api

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxAuditedMessages     = 500
	mailboxAuditReadWindow = 15 * time.Minute
	mailSessionRetention   = 90 * 24 * time.Hour
)

var (
	mailboxAuditReads   = []string{"folder.open", "message.read"}
	mailboxAuditDeletes = []string{"message.delete", "folder.empty"}
)

type mailboxAuditSettings struct {
	Enabled         bool `json:"enabled"`
	LogReads        bool `json:"logReads"`
	LogOwnerDeletes bool `json:"logOwnerDeletes"`
	OwnersCanView   bool `json:"ownersCanView"`
	RetentionDays   int  `json:"retentionDays"`
}

func (st mailboxAuditSettings) covers(role, action string) bool {
	switch {
	case !st.Enabled:
		return false
	case role == "owner" && !(st.LogOwnerDeletes && slices.Contains(mailboxAuditDeletes, action)):
		return false
	case slices.Contains(mailboxAuditReads, action):
		return st.LogReads
	}
	return true
}

func (s *Server) loadMailboxAudit(ctx context.Context, orgID string) (mailboxAuditSettings, error) {
	st := mailboxAuditSettings{LogReads: true, RetentionDays: 180}
	err := s.db.QueryRow(ctx, `SELECT enabled, log_reads, log_owner_deletes, owners_can_view, retention_days FROM mailbox_audit_settings WHERE org_id = $1`, orgID).
		Scan(&st.Enabled, &st.LogReads, &st.LogOwnerDeletes, &st.OwnersCanView, &st.RetentionDays)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return st, err
}

func (s *Server) getMailboxAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	st, err := s.loadMailboxAudit(r.Context(), orgID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) saveMailboxAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in mailboxAuditSettings
	if !decode(w, r, 1<<10, &in) {
		return
	}
	if in.RetentionDays < 1 || in.RetentionDays > 3650 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_retention", "Keep entries for between 1 and 3650 days.")
		return
	}
	_, err := s.db.Exec(r.Context(), `
		INSERT INTO mailbox_audit_settings (org_id, enabled, log_reads, log_owner_deletes, owners_can_view, retention_days) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id) DO UPDATE SET enabled = EXCLUDED.enabled, log_reads = EXCLUDED.log_reads, log_owner_deletes = EXCLUDED.log_owner_deletes,
			owners_can_view = EXCLUDED.owners_can_view, retention_days = EXCLUDED.retention_days, updated_at = now()`,
		orgID, in.Enabled, in.LogReads, in.LogOwnerDeletes, in.OwnersCanView, in.RetentionDays)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "mailbox_audit.update", "organization", orgID, map[string]any{
		"enabled": in.Enabled, "logReads": in.LogReads, "logOwnerDeletes": in.LogOwnerDeletes, "ownersCanView": in.OwnersCanView, "retentionDays": in.RetentionDays,
	})
	writeJSON(w, http.StatusOK, in)
}

type accessItem struct {
	UID, FetchUID uint32
	Subject       string
	MessageID     string
}

type accessRecord struct {
	OrgID, MailboxID, Address string
	ActorID                   *string
	ActorEmail, Role          string
	Client, IP                string
	Action, Folder            string
	Items                     []accessItem
	Meta                      map[string]any
	FetchAddress, FetchFolder string
}

func (s *Server) writeAccess(ctx context.Context, rec accessRecord) {
	if rec.Meta == nil {
		rec.Meta = map[string]any{}
	}
	if len(rec.Items) == 0 {
		rec.Items = []accessItem{{}}
	}
	actor := rec.ActorEmail
	if rec.ActorID != nil {
		actor = *rec.ActorID
	}
	var items []accessItem
	for _, it := range rec.Items {
		if slices.Contains(mailboxAuditReads, rec.Action) {
			key := strings.Join([]string{"aether:mailaudit", actor, rec.MailboxID, rec.Action, rec.Folder, strconv.FormatUint(uint64(it.UID), 10)}, ":")
			if fresh, err := s.kv.SetNX(ctx, key, 1, mailboxAuditReadWindow).Result(); err == nil && !fresh {
				continue
			}
		}
		items = append(items, it)
	}
	s.completeEnvelopes(ctx, &rec, items)
	batch := &pgx.Batch{}
	for _, it := range items {
		meta := rec.Meta
		if it.UID != 0 {
			meta = map[string]any{"uid": it.UID}
			if it.FetchUID != 0 && it.FetchUID != it.UID {
				meta["toUid"] = it.FetchUID
			}
			for k, v := range rec.Meta {
				meta[k] = v
			}
		}
		batch.Queue(`INSERT INTO mailbox_access_events (org_id, mailbox_id, mailbox_address, actor_user_id, actor_email, actor_role, action, folder, subject, message_id, client, ip, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			rec.OrgID, rec.MailboxID, rec.Address, rec.ActorID, rec.ActorEmail, rec.Role, rec.Action, rec.Folder, it.Subject, it.MessageID, rec.Client, rec.IP, meta)
	}
	if batch.Len() == 0 {
		return
	}
	if err := s.db.SendBatch(ctx, batch).Close(); err != nil {
		s.log.Error("mailbox access audit write failed", "mailbox", rec.MailboxID, "action", rec.Action, "err", err)
	}
}

func (s *Server) completeEnvelopes(ctx context.Context, rec *accessRecord, items []accessItem) {
	missing := func() (uids []uint32, at map[uint32][]int) {
		at = map[uint32][]int{}
		for i, it := range items {
			if it.UID != 0 && it.Subject == "" && it.MessageID == "" {
				key := cmp.Or(it.FetchUID, it.UID)
				if rec.FetchAddress == "" {
					key = it.UID
				}
				uids, at[key] = append(uids, key), append(at[key], i)
			}
		}
		return uids, at
	}
	if uids, at := missing(); rec.FetchAddress != "" && len(uids) > 0 {
		envelopes, err := s.mail.Envelopes(rec.FetchAddress, rec.FetchFolder, uids)
		if err != nil {
			s.log.Warn("reading subjects for the mailbox access audit failed", "mailbox", rec.MailboxID, "err", err)
		}
		for _, e := range envelopes {
			for _, i := range at[e.UID] {
				items[i].Subject, items[i].MessageID = e.Subject, e.MessageID
			}
		}
	}
	rec.FetchAddress = ""
	uids, at := missing()
	if len(uids) == 0 || rec.Client == "web" {
		return
	}
	known := make([]int64, len(uids))
	for i, uid := range uids {
		known[i] = int64(uid)
	}
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT ON (u.uid) u.uid, e.subject, e.message_id
		FROM unnest($3::bigint[]) AS u(uid) JOIN mailbox_access_events e ON e.mailbox_id = $1 AND (e.subject <> '' OR e.message_id <> '')
			AND ((e.folder = $2 AND e.metadata->>'uid' = u.uid::text) OR (e.metadata->>'to' = $2 AND e.metadata->>'toUid' = u.uid::text))
		ORDER BY u.uid, e.id DESC`, rec.MailboxID, rec.Folder, known)
	if err != nil {
		s.log.Warn("recalling subjects for the mailbox access audit failed", "mailbox", rec.MailboxID, "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		var subject, messageID string
		if rows.Scan(&uid, &subject, &messageID) == nil {
			for _, i := range at[uint32(uid)] {
				items[i].Subject, items[i].MessageID = subject, messageID
			}
		}
	}
}

func (s *Server) accessRole(ctx context.Context, p *principal, m *ownedMailbox) string {
	switch {
	case m.Access == "owner":
		return "owner"
	case m.Access == "full" || m.Access == "read":
		if m.Kind == "shared" {
			return "member"
		}
		return "delegate"
	case p.apiKey == nil:
		var owner bool
		s.db.QueryRow(ctx, `SELECT coalesce(owner_user_id = $2, false) FROM mailboxes WHERE id = $1`, m.ID, p.User.ID).Scan(&owner)
		if owner {
			return "owner"
		}
	}
	return "admin"
}

func (s *Server) webAccessRecord(ctx context.Context, r *http.Request, p *principal, m *ownedMailbox, action string) (accessRecord, bool) {
	st, err := s.loadMailboxAudit(ctx, m.OrgID)
	if err != nil {
		s.log.Error("loading mailbox audit settings failed", "org", m.OrgID, "err", err)
		return accessRecord{}, false
	}
	if !st.Enabled {
		return accessRecord{}, false
	}
	role := s.accessRole(ctx, p, m)
	if !st.covers(role, action) {
		return accessRecord{}, false
	}
	rec := accessRecord{OrgID: m.OrgID, MailboxID: m.ID, Address: m.Address, Role: role, Client: "web", IP: s.clientIP(r), Action: action, Meta: map[string]any{}}
	if p.apiKey != nil {
		rec.ActorEmail = "API key " + p.apiKey.Name
		rec.Meta["apiKeyId"] = p.apiKey.ID
	} else {
		rec.ActorID, rec.ActorEmail = &p.User.ID, p.User.Email
	}
	return rec, true
}

func (s *Server) recordAccess(r *http.Request, p *principal, m *ownedMailbox, action, folder string, items []accessItem, meta map[string]any) {
	rec, ok := s.webAccessRecord(r.Context(), r, p, m, action)
	if !ok {
		return
	}
	rec.Folder, rec.Items = folder, items
	for k, v := range meta {
		rec.Meta[k] = v
	}
	s.writeAccess(r.Context(), rec)
}

func (s *Server) accessAudit(r *http.Request, p *principal, m *ownedMailbox, action, folder string, uids ...uint32) func(meta map[string]any) {
	rec, ok := s.webAccessRecord(r.Context(), r, p, m, action)
	if !ok {
		return func(map[string]any) {}
	}
	rec.Folder = folder
	if len(uids) > 0 {
		envelopes, err := s.mail.Envelopes(m.Address, folder, uids)
		if err != nil {
			s.log.Warn("reading subjects for the mailbox access audit failed", "mailbox", m.ID, "err", err)
		}
		found := map[uint32]bool{}
		for _, e := range envelopes {
			rec.Items = append(rec.Items, accessItem{UID: e.UID, Subject: e.Subject, MessageID: e.MessageID})
			found[e.UID] = true
		}
		for _, uid := range uids {
			if !found[uid] {
				rec.Items = append(rec.Items, accessItem{UID: uid})
			}
		}
	}
	return func(meta map[string]any) {
		for k, v := range meta {
			rec.Meta[k] = v
		}
		s.writeAccess(r.Context(), rec)
	}
}

func sendAction(m *ownedMailbox) string {
	if m.SendAs {
		return "message.send_as"
	}
	return "message.send_on_behalf"
}

type accessEvent struct {
	ID         int64          `json:"id"`
	CreatedAt  time.Time      `json:"createdAt"`
	ActorEmail string         `json:"actorEmail"`
	ActorRole  string         `json:"actorRole"`
	Action     string         `json:"action"`
	Folder     string         `json:"folder"`
	Subject    string         `json:"subject"`
	MessageID  string         `json:"messageId"`
	Client     string         `json:"client"`
	IP         string         `json:"ip"`
	Metadata   map[string]any `json:"metadata"`
}

type accessSummary struct {
	ActorEmail string    `json:"actorEmail"`
	ActorRole  string    `json:"actorRole"`
	Events     int       `json:"events"`
	First      time.Time `json:"first"`
	Last       time.Time `json:"last"`
}

func (s *Server) mailboxAccessLog(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := pathID(w, r, "mailboxId")
	if !ok {
		return
	}
	var orgID, address string
	var owner *string
	err := s.db.QueryRow(r.Context(), `SELECT org_id, address, owner_user_id FROM mailboxes WHERE id = $1`, id).Scan(&orgID, &address, &owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.orgRole(orgID) == "" && !(owner != nil && *owner == p.User.ID)) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !p.canAdminOrg(orgID) {
		st, err := s.loadMailboxAudit(r.Context(), orgID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if owner == nil || *owner != p.User.ID || !st.OwnersCanView {
			forbidden(w)
			return
		}
	}
	q := r.URL.Query()
	since, sinceErr := parseDate(q.Get("since"))
	until, untilErr := parseDate(q.Get("until"))
	if sinceErr != nil || untilErr != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_date", "Write since and until as dates like 2026-10-31.")
		return
	}
	var untilBound *time.Time
	if !until.IsZero() {
		next := until.AddDate(0, 0, 1)
		untilBound = &next
	}
	before, err := strconv.ParseInt(q.Get("before"), 10, 64)
	if err != nil || before <= 0 {
		before = 1<<63 - 1
	}
	const filter = `mailbox_id = $1 AND actor_role <> 'owner' AND created_at >= $2 AND ($3::timestamptz IS NULL OR created_at < $3)`
	rows, err := s.db.Query(r.Context(), `
		SELECT id, created_at, actor_email, actor_role, action, folder, subject, message_id, client, ip, metadata
		FROM mailbox_access_events WHERE `+filter+` AND id < $4 ORDER BY id DESC LIMIT 100`, id, since, untilBound, before)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[accessEvent])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err = s.db.Query(r.Context(), `
		SELECT actor_email, actor_role, count(*)::int, min(created_at), max(created_at)
		FROM mailbox_access_events WHERE `+filter+` GROUP BY actor_email, actor_role ORDER BY max(created_at) DESC LIMIT 200`, id, since, untilBound)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	summary, err := pgx.CollectRows(rows, pgx.RowToStructByPos[accessSummary])
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"address": address, "summary": nonNil(summary), "events": nonNil(events)})
}

func (s *Server) pruneMailboxAudit(ctx context.Context) {
	if _, err := s.db.Exec(ctx, `
		DELETE FROM mailbox_access_events WHERE id IN (
			SELECT e.id FROM mailbox_access_events e JOIN mailbox_audit_settings st ON st.org_id = e.org_id
			WHERE e.created_at < now() - make_interval(days => st.retention_days) LIMIT 10000)`); err != nil {
		s.log.Error("pruning the mailbox access audit failed", "err", err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM mail_sessions WHERE created_at < $1`, time.Now().Add(-mailSessionRetention)); err != nil {
		s.log.Error("pruning mail sessions failed", "err", err)
	}
}

func (s *Server) recordScheduledSend(ctx context.Context, mailboxID, senderID, mode, subject string, raw []byte, recipients int) {
	var orgID, address, kind, email string
	var owner *string
	err := s.db.QueryRow(ctx, `SELECT m.org_id, m.address, m.kind, m.owner_user_id, u.email FROM mailboxes m JOIN users u ON u.id = $2 WHERE m.id = $1`, mailboxID, senderID).
		Scan(&orgID, &address, &kind, &owner, &email)
	if err != nil {
		s.log.Error("loading a scheduled sender for the mailbox access audit failed", "mailbox", mailboxID, "err", err)
		return
	}
	role := "delegate"
	switch {
	case owner != nil && *owner == senderID:
		role = "owner"
	case kind == "shared":
		role = "member"
	}
	action := "message." + mode
	st, err := s.loadMailboxAudit(ctx, orgID)
	if err != nil || !st.covers(role, action) {
		return
	}
	messageID := ""
	if msg, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		messageID = msg.Header.Get("Message-ID")
	}
	s.writeAccess(ctx, accessRecord{
		OrgID: orgID, MailboxID: mailboxID, Address: address, ActorID: &senderID, ActorEmail: email, Role: role, Client: "web", Action: action,
		Items: []accessItem{{Subject: subject, MessageID: messageID}}, Meta: map[string]any{"recipients": recipients, "scheduled": true},
	})
}

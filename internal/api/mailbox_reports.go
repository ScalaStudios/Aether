package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	mailEventsKey          = "aether:mail_events"
	storageSyncLockID      = 7316251
	storageSyncInterval    = 5 * time.Minute
	storageSyncStartupWait = 10 * time.Second
	maxAliasResolveDepth   = 10
	maxAliasResolveTargets = 5000
)

type mailEvent struct {
	User       string   `json:"u"`
	From       string   `json:"f"`
	Recipients []string `json:"r"`
	Size       int64    `json:"s"`
	Timestamp  int64    `json:"t"`
}

type reportMailboxRef struct {
	ID    string
	OrgID string
}

type reportSummary struct {
	TotalMailboxes     int64 `json:"totalMailboxes"`
	ActiveMailboxes    int64 `json:"activeMailboxes"`
	InactiveMailboxes  int64 `json:"inactiveMailboxes"`
	TotalStorageBytes  int64 `json:"totalStorageBytes"`
	TotalQuotaBytes    int64 `json:"totalQuotaBytes"`
	TotalMessages      int64 `json:"totalMessages"`
	SentTotal          int64 `json:"sentTotal"`
	ReceivedTotal      int64 `json:"receivedTotal"`
	SentBytesTotal     int64 `json:"sentBytesTotal"`
	ReceivedBytesTotal int64 `json:"receivedBytesTotal"`
}

type reportMailbox struct {
	ID               string     `json:"id"`
	Address          string     `json:"address"`
	DisplayName      string     `json:"displayName"`
	Kind             string     `json:"kind"`
	Active           bool       `json:"active"`
	QuotaBytes       int64      `json:"quotaBytes"`
	StorageBytes     int64      `json:"storageBytes"`
	QuotaPercent     *float64   `json:"quotaPercent"`
	MessageCount     int64      `json:"messageCount"`
	StorageUpdatedAt *time.Time `json:"storageUpdatedAt"`
	LastWebLoginAt   *time.Time `json:"lastWebLoginAt"`
	LastIMAPLoginAt  *time.Time `json:"lastImapLoginAt"`
	LastSMTPLoginAt  *time.Time `json:"lastSmtpLoginAt"`
	LastSignInAt     *time.Time `json:"lastSignInAt"`
	SentCount        int64      `json:"sentCount"`
	ReceivedCount    int64      `json:"receivedCount"`
	SentBytes        int64      `json:"sentBytes"`
	ReceivedBytes    int64      `json:"receivedBytes"`
	CreatedAt        time.Time  `json:"createdAt"`
}

type reportActivity struct {
	ID            string `json:"id"`
	Address       string `json:"address"`
	DisplayName   string `json:"displayName"`
	Kind          string `json:"kind"`
	Active        bool   `json:"active"`
	SentCount     int64  `json:"sentCount"`
	ReceivedCount int64  `json:"receivedCount"`
	SentBytes     int64  `json:"sentBytes"`
	ReceivedBytes int64  `json:"receivedBytes"`
}

type volumePoint struct {
	Day           string `json:"day"`
	SentCount     int64  `json:"sentCount"`
	ReceivedCount int64  `json:"receivedCount"`
	SentBytes     int64  `json:"sentBytes"`
	ReceivedBytes int64  `json:"receivedBytes"`
}

type orgReport struct {
	GeneratedAt       time.Time        `json:"generatedAt"`
	StorageUpdatedAt  *time.Time       `json:"storageUpdatedAt"`
	Days              int              `json:"days"`
	InactiveDays      int              `json:"inactiveDays"`
	Summary           reportSummary    `json:"summary"`
	InactiveMailboxes []reportMailbox  `json:"inactiveMailboxes"`
	LargestMailboxes  []reportMailbox  `json:"largestMailboxes"`
	TopSenders        []reportActivity `json:"topSenders"`
	TopReceivers      []reportActivity `json:"topReceivers"`
	VolumeSeries      []volumePoint    `json:"volumeSeries"`
}

const lastSignInExpr = `CASE
	WHEN m.last_web_login_at IS NULL AND m.last_imap_login_at IS NULL AND m.last_smtp_login_at IS NULL THEN NULL
	ELSE greatest(
		coalesce(m.last_web_login_at, '-infinity'::timestamptz),
		coalesce(m.last_imap_login_at, '-infinity'::timestamptz),
		coalesce(m.last_smtp_login_at, '-infinity'::timestamptz)
	)
END`

func (s *Server) touchWebLogin(ctx context.Context, mailboxID string) {
	if _, err := s.db.Exec(ctx, `UPDATE mailboxes SET last_web_login_at = now() WHERE id = $1 AND (last_web_login_at IS NULL OR last_web_login_at < now() - interval '5 minutes')`, mailboxID); err != nil {
		s.log.Warn("updating web activity timestamp failed", "mailbox", mailboxID, "err", err)
	}
}

func (s *Server) recordMailLogin(ctx context.Context, address, service string) {
	col := ""
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "imap", "imaps":
		col = "last_imap_login_at"
	case "smtp", "submission", "submissions":
		col = "last_smtp_login_at"
	default:
		return
	}
	if _, err := s.db.Exec(ctx, `UPDATE mailboxes SET `+col+` = now() WHERE address = $1`, address); err != nil {
		s.log.Warn("recording mail login timestamp failed", "address", address, "service", service, "err", err)
	}
}

func (s *Server) RunReportWorkers(ctx context.Context) {
	go s.consumeMailEvents(ctx)
	s.syncStorageLoop(ctx)
}

func (s *Server) consumeMailEvents(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		res, err := s.kv.BLPop(ctx, 2*time.Second, mailEventsKey).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Warn("reading mail volume events failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if len(res) >= 2 {
			s.handleRawMailEvent(ctx, res[1])
		}
		for range 50 {
			raw, err := s.kv.LPop(ctx, mailEventsKey).Result()
			if err != nil {
				break
			}
			s.handleRawMailEvent(ctx, raw)
		}
	}
}

func (s *Server) handleRawMailEvent(ctx context.Context, raw string) {
	ev, err := parseMailEvent(raw)
	if err != nil {
		s.log.Warn("ignoring invalid mail volume event", "err", err)
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.recordMailEvent(opCtx, ev); err != nil {
		s.log.Warn("recording mail volume event failed", "err", err)
	}
}

func parseMailEvent(raw string) (*mailEvent, error) {
	var ev mailEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return nil, err
	}
	ev.User = normalizeMailAddress(ev.User)
	ev.From = normalizeMailAddress(ev.From)
	if ev.Size < 0 {
		ev.Size = 0
	}
	return &ev, nil
}

func normalizeMailAddress(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if idx := strings.IndexByte(addr, '*'); idx >= 0 {
		addr = addr[:idx]
	}
	return addr
}

func stripDetailAddress(addr string) (string, bool) {
	local, domain, ok := strings.Cut(addr, "@")
	if !ok || local == "" || domain == "" {
		return "", false
	}
	base, _, hasDetail := strings.Cut(local, "+")
	if !hasDetail || base == "" {
		return "", false
	}
	return base + "@" + domain, true
}

func (s *Server) lookupActiveMailbox(ctx context.Context, addr string) (reportMailboxRef, bool, error) {
	if addr == "" {
		return reportMailboxRef{}, false, nil
	}
	candidates := []string{addr}
	if base, ok := stripDetailAddress(addr); ok {
		candidates = append(candidates, base)
	}
	for _, candidate := range candidates {
		var ref reportMailboxRef
		err := s.db.QueryRow(ctx, `
			SELECT m.id, m.org_id FROM mailboxes m
			JOIN domains d ON d.id = m.domain_id
			WHERE m.address = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL`, candidate).Scan(&ref.ID, &ref.OrgID)
		if err == nil {
			return ref, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return reportMailboxRef{}, false, err
		}
	}
	return reportMailboxRef{}, false, nil
}

func (s *Server) lookupAliasDestinations(ctx context.Context, addr string) ([]string, bool, error) {
	candidates := []string{addr}
	if base, ok := stripDetailAddress(addr); ok {
		candidates = append(candidates, base)
	}
	for _, candidate := range candidates {
		var dests []string
		err := s.db.QueryRow(ctx, `
			SELECT a.destinations FROM aliases a
			JOIN domains d ON d.id = a.domain_id
			WHERE a.address = $1 AND a.active AND d.active AND d.verified_at IS NOT NULL`, candidate).Scan(&dests)
		if err == nil {
			return dests, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, err
		}
	}
	return nil, false, nil
}

func (s *Server) lookupCatchAllDestinations(ctx context.Context, domain string) ([]string, bool, error) {
	if domain == "" {
		return nil, false, nil
	}
	var dests []string
	err := s.db.QueryRow(ctx, `
		SELECT a.destinations FROM aliases a
		JOIN domains d ON d.id = a.domain_id
		WHERE d.name = $1 AND a.local_part = '' AND a.active AND d.active AND d.verified_at IS NOT NULL`, domain).Scan(&dests)
	if err == nil {
		return dests, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return nil, false, err
}

func (s *Server) resolveRecipientMailboxes(ctx context.Context, recipients []string) ([]reportMailboxRef, error) {
	type item struct {
		addr  string
		depth int
	}
	queue := make([]item, 0, len(recipients))
	visited := make(map[string]struct{}, len(recipients))
	for _, r := range recipients {
		addr := normalizeMailAddress(r)
		if addr == "" {
			continue
		}
		if _, seen := visited[addr]; seen {
			continue
		}
		visited[addr] = struct{}{}
		queue = append(queue, item{addr: addr, depth: 0})
	}
	found := make(map[string]reportMailboxRef)
	for len(queue) > 0 && len(found) < maxAliasResolveTargets {
		cur := queue[0]
		queue = queue[1:]
		ref, isMailbox, err := s.lookupActiveMailbox(ctx, cur.addr)
		if err != nil {
			return nil, err
		}
		if isMailbox {
			found[ref.ID] = ref
		}
		if cur.depth >= maxAliasResolveDepth {
			continue
		}
		dests, isAlias, err := s.lookupAliasDestinations(ctx, cur.addr)
		if err != nil {
			return nil, err
		}
		if !isAlias && !isMailbox {
			if _, domain, ok := strings.Cut(cur.addr, "@"); ok {
				dests, isAlias, err = s.lookupCatchAllDestinations(ctx, domain)
				if err != nil {
					return nil, err
				}
			}
		}
		if isAlias {
			for _, d := range dests {
				next := normalizeMailAddress(d)
				if next == "" {
					continue
				}
				if _, seen := visited[next]; seen {
					continue
				}
				visited[next] = struct{}{}
				queue = append(queue, item{addr: next, depth: cur.depth + 1})
			}
		}
	}
	out := make([]reportMailboxRef, 0, len(found))
	for _, ref := range found {
		out = append(out, ref)
	}
	return out, nil
}

func (s *Server) recordMailEvent(ctx context.Context, ev *mailEvent) error {
	at := time.Now().UTC()
	if ev.Timestamp > 0 {
		at = time.Unix(ev.Timestamp, 0).UTC()
	}
	day := at.Format(time.DateOnly)

	var sender *reportMailboxRef
	if ev.User != "" {
		if ref, ok, err := s.lookupActiveMailbox(ctx, ev.From); err != nil {
			return err
		} else if ok {
			sender = &ref
		} else if ref, ok, err := s.lookupActiveMailbox(ctx, ev.User); err != nil {
			return err
		} else if ok {
			sender = &ref
		}
	}
	recipients, err := s.resolveRecipientMailboxes(ctx, ev.Recipients)
	if err != nil {
		return err
	}
	if sender == nil && len(recipients) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	if sender != nil {
		batch.Queue(`
			INSERT INTO mailbox_daily_stats (mailbox_id, org_id, day, sent_count, sent_bytes)
			VALUES ($1, $2, $3::date, 1, $4)
			ON CONFLICT (mailbox_id, day) DO UPDATE
			SET sent_count = mailbox_daily_stats.sent_count + 1,
			    sent_bytes = mailbox_daily_stats.sent_bytes + EXCLUDED.sent_bytes`,
			sender.ID, sender.OrgID, day, ev.Size)
	}
	for _, rcpt := range recipients {
		batch.Queue(`
			INSERT INTO mailbox_daily_stats (mailbox_id, org_id, day, received_count, received_bytes)
			VALUES ($1, $2, $3::date, 1, $4)
			ON CONFLICT (mailbox_id, day) DO UPDATE
			SET received_count = mailbox_daily_stats.received_count + 1,
			    received_bytes = mailbox_daily_stats.received_bytes + EXCLUDED.received_bytes`,
			rcpt.ID, rcpt.OrgID, day, ev.Size)
	}
	br := s.db.SendBatch(ctx, batch)
	defer br.Close()
	for range batch.Len() {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) syncStorageLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(storageSyncStartupWait):
	}
	ticker := time.NewTicker(storageSyncInterval)
	defer ticker.Stop()
	for {
		opCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := s.syncAllStorage(opCtx); err != nil && ctx.Err() == nil {
			s.log.Warn("mailbox storage sync failed", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) syncAllStorage(ctx context.Context) error {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, storageSyncLockID).Scan(&locked); err != nil || !locked {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, storageSyncLockID)

	rows, err := s.db.Query(ctx, `
		SELECT m.id, m.address FROM mailboxes m
		JOIN domains d ON d.id = m.domain_id
		WHERE m.active AND d.active AND d.verified_at IS NOT NULL`)
	if err != nil {
		return err
	}
	_, _, err = s.applyStorageRows(ctx, rows)
	return err
}

func (s *Server) syncOrgStorage(ctx context.Context, orgID string) (int, time.Time, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.id, m.address FROM mailboxes m
		JOIN domains d ON d.id = m.domain_id
		WHERE m.org_id = $1 AND m.active AND d.active AND d.verified_at IS NOT NULL`, orgID)
	if err != nil {
		return 0, time.Time{}, err
	}
	return s.applyStorageRows(ctx, rows)
}

func (s *Server) applyStorageRows(ctx context.Context, rows pgx.Rows) (int, time.Time, error) {
	type target struct {
		ID      string
		Address string
	}
	targets, err := pgx.CollectRows(rows, pgx.RowToStructByPos[target])
	if err != nil {
		return 0, time.Time{}, err
	}
	now := time.Now().UTC()
	if len(targets) == 0 {
		return 0, now, nil
	}
	addresses := make([]string, 0, len(targets))
	byAddress := make(map[string]string, len(targets))
	for _, t := range targets {
		addresses = append(addresses, t.Address)
		byAddress[t.Address] = t.ID
	}
	stats, err := s.mail.MailboxStorage(ctx, addresses)
	if err != nil {
		return 0, time.Time{}, err
	}
	batch := &pgx.Batch{}
	updated := 0
	for addr, st := range stats {
		id, ok := byAddress[addr]
		if !ok {
			continue
		}
		batch.Queue(`UPDATE mailboxes SET storage_bytes = $2, message_count = $3, storage_updated_at = $4 WHERE id = $1`,
			id, st.StorageBytes, st.MessageCount, now)
		updated++
	}
	if updated == 0 {
		return 0, now, nil
	}
	br := s.db.SendBatch(ctx, batch)
	defer br.Close()
	for range batch.Len() {
		if _, err := br.Exec(); err != nil {
			return 0, time.Time{}, err
		}
	}
	return updated, now, nil
}

func parseIntParam(raw string, fallback, minVal, maxVal int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < minVal || v > maxVal {
		return fallback
	}
	return v
}

func wantsCSV(r *http.Request) (bool, bool) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "":
		return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/csv"), true
	case "json":
		return false, true
	case "csv":
		return true, true
	default:
		return false, false
	}
}

func quotaPercent(storageBytes, quotaBytes int64) *float64 {
	if quotaBytes <= 0 {
		return nil
	}
	pct := float64(storageBytes) * 100 / float64(quotaBytes)
	pct = float64(int64(pct*10+0.5)) / 10
	return &pct
}

func scanReportMailbox(row pgx.CollectableRow) (reportMailbox, error) {
	var m reportMailbox
	err := row.Scan(
		&m.ID, &m.Address, &m.DisplayName, &m.Kind, &m.Active,
		&m.QuotaBytes, &m.StorageBytes, &m.MessageCount, &m.StorageUpdatedAt,
		&m.LastWebLoginAt, &m.LastIMAPLoginAt, &m.LastSMTPLoginAt, &m.LastSignInAt,
		&m.SentCount, &m.ReceivedCount, &m.SentBytes, &m.ReceivedBytes, &m.CreatedAt,
	)
	if err == nil {
		m.QuotaPercent = quotaPercent(m.StorageBytes, m.QuotaBytes)
	}
	return m, err
}

func (s *Server) queryReportMailboxes(ctx context.Context, orgID string, days int, where, orderBy string, limit int, extraArgs ...any) ([]reportMailbox, error) {
	args := []any{orgID, days - 1}
	args = append(args, extraArgs...)
	limitClause := ""
	if limit > 0 {
		args = append(args, limit)
		limitClause = fmt.Sprintf(" LIMIT $%d", len(args))
	}
	q := `
		SELECT m.id, m.address, m.display_name, m.kind, m.active,
		       m.quota_bytes, m.storage_bytes, m.message_count, m.storage_updated_at,
		       m.last_web_login_at, m.last_imap_login_at, m.last_smtp_login_at, ` + lastSignInExpr + `,
		       coalesce(v.sent_count, 0)::bigint, coalesce(v.received_count, 0)::bigint,
		       coalesce(v.sent_bytes, 0)::bigint, coalesce(v.received_bytes, 0)::bigint, m.created_at
		FROM mailboxes m
		LEFT JOIN (
			SELECT mailbox_id,
			       sum(sent_count) AS sent_count, sum(received_count) AS received_count,
			       sum(sent_bytes) AS sent_bytes, sum(received_bytes) AS received_bytes
			FROM mailbox_daily_stats
			WHERE org_id = $1 AND day >= (now() AT TIME ZONE 'UTC')::date - $2::int
			GROUP BY mailbox_id
		) v ON v.mailbox_id = m.id
		WHERE m.org_id = $1` + where + `
		ORDER BY ` + orderBy + limitClause
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, scanReportMailbox)
	return nonNil(items), err
}

func (s *Server) queryReportActivity(ctx context.Context, orgID string, days int, sortBy string, limit int) ([]reportActivity, error) {
	filter := "v.sent_count > 0 OR v.received_count > 0"
	order := "v.sent_count DESC, v.sent_bytes DESC, m.address ASC"
	switch sortBy {
	case "sent":
		filter = "v.sent_count > 0"
		order = "v.sent_count DESC, v.sent_bytes DESC, m.address ASC"
	case "received":
		filter = "v.received_count > 0"
		order = "v.received_count DESC, v.received_bytes DESC, m.address ASC"
	case "total":
		order = "(v.sent_count + v.received_count) DESC, (v.sent_bytes + v.received_bytes) DESC, m.address ASC"
	}
	args := []any{orgID, days - 1}
	limitClause := ""
	if limit > 0 {
		args = append(args, limit)
		limitClause = fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := s.db.Query(ctx, `
		SELECT m.id, m.address, m.display_name, m.kind, m.active,
		       v.sent_count::bigint, v.received_count::bigint, v.sent_bytes::bigint, v.received_bytes::bigint
		FROM mailboxes m
		JOIN (
			SELECT mailbox_id,
			       sum(sent_count) AS sent_count, sum(received_count) AS received_count,
			       sum(sent_bytes) AS sent_bytes, sum(received_bytes) AS received_bytes
			FROM mailbox_daily_stats
			WHERE org_id = $1 AND day >= (now() AT TIME ZONE 'UTC')::date - $2::int
			GROUP BY mailbox_id
		) v ON v.mailbox_id = m.id
		WHERE m.org_id = $1 AND (`+filter+`)
		ORDER BY `+order+limitClause, args...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[reportActivity])
	return nonNil(items), err
}

func (s *Server) queryVolumeSeries(ctx context.Context, orgID string, days int, now time.Time) ([]volumePoint, error) {
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -(days - 1))
	rows, err := s.db.Query(ctx, `
		SELECT to_char(day, 'YYYY-MM-DD'),
		       coalesce(sum(sent_count), 0)::bigint, coalesce(sum(received_count), 0)::bigint,
		       coalesce(sum(sent_bytes), 0)::bigint, coalesce(sum(received_bytes), 0)::bigint
		FROM mailbox_daily_stats
		WHERE org_id = $1 AND day >= $2::date AND day <= $3::date
		GROUP BY day
		ORDER BY day ASC`, orgID, start.Format(time.DateOnly), today.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	raw, err := pgx.CollectRows(rows, pgx.RowToStructByPos[volumePoint])
	if err != nil {
		return nil, err
	}
	return fillVolumeSeries(start, days, raw), nil
}

func fillVolumeSeries(start time.Time, days int, raw []volumePoint) []volumePoint {
	byDay := make(map[string]volumePoint, len(raw))
	for _, p := range raw {
		byDay[p.Day] = p
	}
	out := make([]volumePoint, 0, days)
	for i := range days {
		day := start.AddDate(0, 0, i).Format(time.DateOnly)
		if p, ok := byDay[day]; ok {
			out = append(out, p)
		} else {
			out = append(out, volumePoint{Day: day})
		}
	}
	return out
}

func (s *Server) queryReportSummary(ctx context.Context, orgID string, days, inactiveDays int) (reportSummary, *time.Time, error) {
	var sum reportSummary
	var updatedAt *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT count(*)::bigint,
		       count(*) FILTER (WHERE active)::bigint,
		       count(*) FILTER (WHERE active AND kind = 'user' AND (`+lastSignInExpr+` IS NULL OR `+lastSignInExpr+` < now() - make_interval(days => $2)))::bigint,
		       coalesce(sum(storage_bytes), 0)::bigint,
		       coalesce(sum(quota_bytes), 0)::bigint,
		       coalesce(sum(message_count), 0)::bigint,
		       max(storage_updated_at)
		FROM mailboxes m
		WHERE m.org_id = $1`, orgID, inactiveDays).
		Scan(&sum.TotalMailboxes, &sum.ActiveMailboxes, &sum.InactiveMailboxes, &sum.TotalStorageBytes, &sum.TotalQuotaBytes, &sum.TotalMessages, &updatedAt)
	if err != nil {
		return sum, nil, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT coalesce(sum(sent_count), 0)::bigint, coalesce(sum(received_count), 0)::bigint,
		       coalesce(sum(sent_bytes), 0)::bigint, coalesce(sum(received_bytes), 0)::bigint
		FROM mailbox_daily_stats
		WHERE org_id = $1 AND day >= (now() AT TIME ZONE 'UTC')::date - $2::int`, orgID, days-1).
		Scan(&sum.SentTotal, &sum.ReceivedTotal, &sum.SentBytesTotal, &sum.ReceivedBytesTotal)
	return sum, updatedAt, err
}

func (s *Server) getOrgReport(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	days := parseIntParam(q.Get("days"), 30, 1, 366)
	inactiveDays := parseIntParam(q.Get("inactiveDays"), 30, 1, 3650)
	limit := parseIntParam(q.Get("limit"), 25, 1, 500)
	now := time.Now().UTC()

	summary, storageUpdatedAt, err := s.queryReportSummary(r.Context(), orgID, days, inactiveDays)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	inactive, err := s.queryReportMailboxes(r.Context(), orgID, days,
		` AND m.active AND m.kind = 'user' AND (`+lastSignInExpr+` IS NULL OR `+lastSignInExpr+` < now() - make_interval(days => $3))`,
		lastSignInExpr+` ASC NULLS FIRST, m.created_at ASC, m.address ASC`, limit, inactiveDays)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	largest, err := s.queryReportMailboxes(r.Context(), orgID, days, ``, `m.storage_bytes DESC, m.address ASC`, limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	topSenders, err := s.queryReportActivity(r.Context(), orgID, days, "sent", limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	topReceivers, err := s.queryReportActivity(r.Context(), orgID, days, "received", limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	volumeSeries, err := s.queryVolumeSeries(r.Context(), orgID, days, now)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, orgReport{
		GeneratedAt:       now,
		StorageUpdatedAt:  storageUpdatedAt,
		Days:              days,
		InactiveDays:      inactiveDays,
		Summary:           summary,
		InactiveMailboxes: inactive,
		LargestMailboxes:  largest,
		TopSenders:        topSenders,
		TopReceivers:      topReceivers,
		VolumeSeries:      volumeSeries,
	})
}

func (s *Server) getOrgSubReport(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	report := strings.ToLower(r.PathValue("report"))
	asCSV, validFormat := wantsCSV(r)
	if !validFormat {
		writeError(w, http.StatusUnprocessableEntity, "invalid_format", "Format must be json or csv.")
		return
	}
	q := r.URL.Query()
	days := parseIntParam(q.Get("days"), 30, 1, 366)
	inactiveDays := parseIntParam(q.Get("inactiveDays"), 30, 1, 3650)
	defaultLimit := 200
	if asCSV {
		defaultLimit = 0
	}
	limit := parseIntParam(q.Get("limit"), defaultLimit, 1, 10000)
	if q.Get("limit") == "" && asCSV {
		limit = 0
	}

	var slug string
	if asCSV {
		if err := s.db.QueryRow(r.Context(), `SELECT slug FROM organizations WHERE id = $1`, orgID).Scan(&slug); err != nil {
			s.internalError(w, r, err)
			return
		}
	}

	now := time.Now().UTC()
	switch report {
	case "mailboxes":
		items, err := s.queryReportMailboxes(r.Context(), orgID, days, ``, `m.address ASC`, limit)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if asCSV {
			writeMailboxCSV(w, slug, "mailboxes", now, items)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"generatedAt": now, "days": days, "mailboxes": items})
	case "inactive":
		items, err := s.queryReportMailboxes(r.Context(), orgID, days,
			` AND m.active AND m.kind = 'user' AND (`+lastSignInExpr+` IS NULL OR `+lastSignInExpr+` < now() - make_interval(days => $3))`,
			lastSignInExpr+` ASC NULLS FIRST, m.created_at ASC, m.address ASC`, limit, inactiveDays)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if asCSV {
			writeMailboxCSV(w, slug, "inactive-mailboxes", now, items)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"generatedAt": now, "inactiveDays": inactiveDays, "mailboxes": items})
	case "largest":
		items, err := s.queryReportMailboxes(r.Context(), orgID, days, ``, `m.storage_bytes DESC, m.address ASC`, limit)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if asCSV {
			writeMailboxCSV(w, slug, "largest-mailboxes", now, items)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"generatedAt": now, "mailboxes": items})
	case "activity":
		sortBy := strings.ToLower(strings.TrimSpace(q.Get("sort")))
		if sortBy == "" {
			sortBy = "total"
		}
		if sortBy != "sent" && sortBy != "received" && sortBy != "total" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_sort", "Sort must be sent, received, or total.")
			return
		}
		items, err := s.queryReportActivity(r.Context(), orgID, days, sortBy, limit)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if asCSV {
			writeActivityCSV(w, slug, "mail-activity", now, items)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"generatedAt": now, "days": days, "sort": sortBy, "mailboxes": items})
	case "volume":
		series, err := s.queryVolumeSeries(r.Context(), orgID, days, now)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if asCSV {
			writeVolumeCSV(w, slug, "mail-volume", now, series)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"generatedAt": now, "days": days, "series": series})
	default:
		notFound(w)
	}
}

func (s *Server) refreshOrgStorage(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	if !s.allow(r.Context(), "report-refresh:"+orgID, 10) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Wait a moment before refreshing mailbox sizes again.")
		return
	}
	opCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	updated, at, err := s.syncOrgStorage(opCtx, orgID)
	if err != nil {
		s.log.Warn("refreshing organization mailbox sizes failed", "org", orgID, "err", err)
		writeError(w, http.StatusServiceUnavailable, "mail_unavailable", "The mail server could not be reached to read mailbox sizes. Try again in a moment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "storageUpdatedAt": at})
}

func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func formatTimeCSV(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatFloatCSV(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}

func writeCSVResponse(w http.ResponseWriter, filename string, headers []string, rows [][]string) {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.Write(headers)
	for _, row := range rows {
		cw.Write(row)
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", mailpkg.ContentDisposition(filename))
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

func writeMailboxCSV(w http.ResponseWriter, slug, kind string, now time.Time, items []reportMailbox) {
	headers := []string{
		"address", "display_name", "kind", "active",
		"storage_bytes", "quota_bytes", "quota_percent", "message_count", "storage_updated_at",
		"last_sign_in_at", "last_web_login_at", "last_imap_login_at", "last_smtp_login_at",
		"sent_count", "received_count", "sent_bytes", "received_bytes", "created_at",
	}
	rows := make([][]string, 0, len(items))
	for _, m := range items {
		rows = append(rows, []string{
			csvSafe(m.Address),
			csvSafe(m.DisplayName),
			m.Kind,
			strconv.FormatBool(m.Active),
			strconv.FormatInt(m.StorageBytes, 10),
			strconv.FormatInt(m.QuotaBytes, 10),
			formatFloatCSV(m.QuotaPercent),
			strconv.FormatInt(m.MessageCount, 10),
			formatTimeCSV(m.StorageUpdatedAt),
			formatTimeCSV(m.LastSignInAt),
			formatTimeCSV(m.LastWebLoginAt),
			formatTimeCSV(m.LastIMAPLoginAt),
			formatTimeCSV(m.LastSMTPLoginAt),
			strconv.FormatInt(m.SentCount, 10),
			strconv.FormatInt(m.ReceivedCount, 10),
			strconv.FormatInt(m.SentBytes, 10),
			strconv.FormatInt(m.ReceivedBytes, 10),
			m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeCSVResponse(w, slug+"-"+kind+"-"+now.Format(time.DateOnly)+".csv", headers, rows)
}

func writeActivityCSV(w http.ResponseWriter, slug, kind string, now time.Time, items []reportActivity) {
	headers := []string{"address", "display_name", "kind", "active", "sent_count", "received_count", "sent_bytes", "received_bytes"}
	rows := make([][]string, 0, len(items))
	for _, a := range items {
		rows = append(rows, []string{
			csvSafe(a.Address),
			csvSafe(a.DisplayName),
			a.Kind,
			strconv.FormatBool(a.Active),
			strconv.FormatInt(a.SentCount, 10),
			strconv.FormatInt(a.ReceivedCount, 10),
			strconv.FormatInt(a.SentBytes, 10),
			strconv.FormatInt(a.ReceivedBytes, 10),
		})
	}
	writeCSVResponse(w, slug+"-"+kind+"-"+now.Format(time.DateOnly)+".csv", headers, rows)
}

func writeVolumeCSV(w http.ResponseWriter, slug, kind string, now time.Time, series []volumePoint) {
	headers := []string{"day", "sent_count", "received_count", "sent_bytes", "received_bytes"}
	rows := make([][]string, 0, len(series))
	for _, p := range series {
		rows = append(rows, []string{
			p.Day,
			strconv.FormatInt(p.SentCount, 10),
			strconv.FormatInt(p.ReceivedCount, 10),
			strconv.FormatInt(p.SentBytes, 10),
			strconv.FormatInt(p.ReceivedBytes, 10),
		})
	}
	writeCSVResponse(w, slug+"-"+kind+"-"+now.Format(time.DateOnly)+".csv", headers, rows)
}

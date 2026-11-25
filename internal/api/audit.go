package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/config"
)

const (
	defaultAuditPageLimit = 50
	maxAuditPageLimit     = 200
	maxAuditExportLimit   = 50000
	auditPurgeBatchSize   = 5000
	auditPurgeInterval    = time.Hour
	auditPurgeLockID      = 7316249
	maxAuditActionFilters = 25
)

type auditEvent struct {
	ID          int64          `json:"id"`
	CreatedAt   time.Time      `json:"createdAt"`
	OrgID       *string        `json:"orgId"`
	ActorUserID *string        `json:"actorUserId"`
	ActorEmail  *string        `json:"actorEmail"`
	Action      string         `json:"action"`
	TargetType  string         `json:"targetType"`
	TargetID    string         `json:"targetId"`
	IP          string         `json:"ip"`
	Metadata    map[string]any `json:"metadata"`
}

type auditRetention struct {
	RetentionDays          *int `json:"retentionDays"`
	DefaultRetentionDays   int  `json:"defaultRetentionDays"`
	EffectiveRetentionDays int  `json:"effectiveRetentionDays"`
}

func makeAuditRetention(orgDays *int, defaultDays int) auditRetention {
	effective := defaultDays
	if orgDays != nil {
		effective = *orgDays
	}
	return auditRetention{
		RetentionDays:          orgDays,
		DefaultRetentionDays:   defaultDays,
		EffectiveRetentionDays: effective,
	}
}

type auditFilter struct {
	orgScope   string
	orgID      string
	before     int64
	limit      int
	actor      string
	actions    []string
	target     string
	targetType string
	targetID   string
	since      *time.Time
	until      *time.Time
	untilExcl  bool
	query      string
}

func parseAuditTime(raw string, endOfDay bool) (*time.Time, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		u := t.UTC()
		return &u, false, nil
	}
	if t, err := time.Parse(time.DateOnly, raw); err == nil {
		u := t.UTC()
		if endOfDay {
			u = u.AddDate(0, 0, 1)
			return &u, true, nil
		}
		return &u, false, nil
	}
	return nil, false, errors.New("invalid time")
}

func parseAuditFilter(q url.Values, defaultLimit, maxLimit int) (auditFilter, string, string) {
	f := auditFilter{
		limit:      defaultLimit,
		actor:      strings.TrimSpace(q.Get("actor")),
		target:     strings.TrimSpace(q.Get("target")),
		targetType: strings.TrimSpace(q.Get("targetType")),
		targetID:   strings.TrimSpace(q.Get("targetId")),
		query:      strings.TrimSpace(q.Get("q")),
	}
	if raw := strings.TrimSpace(q.Get("before")); raw != "" {
		if before, err := strconv.ParseInt(raw, 10, 64); err == nil && before > 0 {
			f.before = before
		}
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			f.limit = min(n, maxLimit)
		}
	}
	for _, raw := range q["action"] {
		for _, part := range strings.Split(raw, ",") {
			a := strings.TrimSpace(part)
			if a == "" || slices.Contains(f.actions, a) {
				continue
			}
			f.actions = append(f.actions, a)
			if len(f.actions) >= maxAuditActionFilters {
				break
			}
		}
		if len(f.actions) >= maxAuditActionFilters {
			break
		}
	}
	since, _, err := parseAuditTime(q.Get("since"), false)
	if err != nil {
		return f, "invalid_date", "Write since and until as RFC3339 timestamps or YYYY-MM-DD dates."
	}
	until, untilExcl, err := parseAuditTime(q.Get("until"), true)
	if err != nil {
		return f, "invalid_date", "Write since and until as RFC3339 timestamps or YYYY-MM-DD dates."
	}
	if since != nil && until != nil {
		if (untilExcl && !since.Before(*until)) || (!untilExcl && since.After(*until)) {
			return f, "invalid_date", "since must be on or before until."
		}
	}
	f.since = since
	f.until = until
	f.untilExcl = untilExcl
	return f, "", ""
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func buildAuditQuery(f auditFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	switch f.orgScope {
	case "org":
		conds = append(conds, "e.org_id = "+add(f.orgID))
	case "none":
		conds = append(conds, "e.org_id IS NULL")
	}
	if f.before > 0 {
		conds = append(conds, "e.id < "+add(f.before))
	}
	if f.actor != "" {
		lower := strings.ToLower(f.actor)
		if uuidPattern.MatchString(lower) {
			conds = append(conds, fmt.Sprintf("(e.actor_user_id = %s OR e.metadata->>'apiKeyId' = %s)", add(lower), add(lower)))
		} else {
			like := add("%" + escapeLike(f.actor) + "%")
			exact := add(f.actor)
			conds = append(conds, fmt.Sprintf(`(coalesce(u.email, '') ILIKE %s ESCAPE '\' OR coalesce(e.metadata->>'apiKeyName', '') ILIKE %s ESCAPE '\' OR coalesce(e.metadata->>'apiKeyId', '') = %s)`, like, like, exact))
		}
	}
	if len(f.actions) > 0 {
		var exact []string
		var actionParts []string
		for _, a := range f.actions {
			if strings.HasSuffix(a, "*") {
				prefix := strings.TrimSuffix(a, "*")
				if prefix != "" {
					actionParts = append(actionParts, fmt.Sprintf(`e.action LIKE %s ESCAPE '\'`, add(escapeLike(prefix)+"%")))
				}
			} else {
				exact = append(exact, a)
			}
		}
		if len(exact) > 0 {
			actionParts = append(actionParts, fmt.Sprintf("e.action = ANY(%s)", add(exact)))
		}
		if len(actionParts) > 0 {
			conds = append(conds, "("+strings.Join(actionParts, " OR ")+")")
		}
	}
	if f.targetType != "" {
		conds = append(conds, "e.target_type = "+add(f.targetType))
	}
	if f.targetID != "" {
		conds = append(conds, "e.target_id = "+add(f.targetID))
	}
	if f.target != "" {
		like := add("%" + escapeLike(f.target) + "%")
		conds = append(conds, fmt.Sprintf(`(e.target_id ILIKE %s ESCAPE '\' OR e.target_type ILIKE %s ESCAPE '\' OR (e.target_type || ':' || e.target_id) ILIKE %s ESCAPE '\' OR coalesce(e.metadata->>'address', '') ILIKE %s ESCAPE '\')`, like, like, like, like))
	}
	if f.since != nil {
		conds = append(conds, "e.created_at >= "+add(*f.since))
	}
	if f.until != nil {
		if f.untilExcl {
			conds = append(conds, "e.created_at < "+add(*f.until))
		} else {
			conds = append(conds, "e.created_at <= "+add(*f.until))
		}
	}
	if f.query != "" {
		tsq := add(f.query)
		like := add("%" + escapeLike(f.query) + "%")
		conds = append(conds, fmt.Sprintf(`(e.search_vector @@ websearch_to_tsquery('simple', %s) OR coalesce(u.email, '') ILIKE %s ESCAPE '\' OR e.action ILIKE %s ESCAPE '\' OR e.target_type ILIKE %s ESCAPE '\' OR e.target_id ILIKE %s ESCAPE '\' OR e.ip ILIKE %s ESCAPE '\' OR e.metadata::text ILIKE %s ESCAPE '\')`, tsq, like, like, like, like, like, like))
	}
	var b strings.Builder
	b.WriteString("SELECT e.id, e.created_at, e.org_id, e.actor_user_id, u.email, e.action, e.target_type, e.target_id, e.ip, e.metadata FROM audit_events e LEFT JOIN users u ON u.id = e.actor_user_id")
	if len(conds) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(conds, " AND "))
	}
	b.WriteString(" ORDER BY e.id DESC LIMIT ")
	b.WriteString(add(f.limit))
	return b.String(), args
}

func scanAuditEvent(rows pgx.Rows) (auditEvent, error) {
	var ev auditEvent
	var metaRaw []byte
	if err := rows.Scan(&ev.ID, &ev.CreatedAt, &ev.OrgID, &ev.ActorUserID, &ev.ActorEmail, &ev.Action, &ev.TargetType, &ev.TargetID, &ev.IP, &metaRaw); err != nil {
		return ev, err
	}
	ev.Metadata = map[string]any{}
	if len(metaRaw) > 0 {
		_ = json.Unmarshal(metaRaw, &ev.Metadata)
	}
	return ev, nil
}

func (s *Server) queryAuditEvents(ctx context.Context, f auditFilter) ([]auditEvent, error) {
	sql, args := buildAuditQuery(f)
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []auditEvent{}
	for rows.Next() {
		ev, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permAuditRead)
	if !ok {
		return
	}
	f, code, msg := parseAuditFilter(r.URL.Query(), defaultAuditPageLimit, maxAuditPageLimit)
	if code != "" {
		writeError(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	f.orgScope = "org"
	f.orgID = orgID
	events, err := s.queryAuditEvents(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) exportAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permAuditRead)
	if !ok {
		return
	}
	f, code, msg := parseAuditFilter(r.URL.Query(), maxAuditExportLimit, maxAuditExportLimit)
	if code != "" {
		writeError(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	f.orgScope = "org"
	f.orgID = orgID
	s.serveAuditExport(w, r, p, f, &orgID)
}

func (s *Server) listAdminAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	f, code, msg := parseAuditFilter(r.URL.Query(), defaultAuditPageLimit, maxAuditPageLimit)
	if code != "" {
		writeError(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	if !applyAdminAuditOrgScope(w, r, &f) {
		return
	}
	events, err := s.queryAuditEvents(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) exportAdminAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	if !s.requirePlatformAdmin(w, p) {
		return
	}
	f, code, msg := parseAuditFilter(r.URL.Query(), maxAuditExportLimit, maxAuditExportLimit)
	if code != "" {
		writeError(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	if !applyAdminAuditOrgScope(w, r, &f) {
		return
	}
	var auditOrg *string
	if f.orgScope == "org" {
		id := f.orgID
		auditOrg = &id
	}
	s.serveAuditExport(w, r, p, f, auditOrg)
}

func applyAdminAuditOrgScope(w http.ResponseWriter, r *http.Request, f *auditFilter) bool {
	raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("orgId")))
	switch {
	case raw == "":
		f.orgScope = "all"
	case raw == "none":
		f.orgScope = "none"
	case uuidPattern.MatchString(raw):
		f.orgScope = "org"
		f.orgID = raw
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid_org", "orgId must be an organization UUID or none.")
		return false
	}
	return true
}

func (s *Server) serveAuditExport(w http.ResponseWriter, r *http.Request, p *principal, f auditFilter, auditOrg *string) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "csv" && format != "json" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_format", "format must be csv or json.")
		return
	}
	sql, args := buildAuditQuery(f)
	rows, err := s.db.Query(r.Context(), sql, args...)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer rows.Close()

	next := func() (*auditEvent, error) {
		if !rows.Next() {
			return nil, rows.Err()
		}
		ev, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		return &ev, nil
	}
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit-log.csv"`)
		w.WriteHeader(http.StatusOK)
		if err := writeAuditEventsCSV(w, next); err != nil {
			s.log.Warn("audit csv export failed", "err", err)
			return
		}
	} else {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit-log.json"`)
		w.WriteHeader(http.StatusOK)
		if err := writeAuditEventsJSON(w, next); err != nil {
			s.log.Warn("audit json export failed", "err", err)
			return
		}
	}
	meta := map[string]any{"format": format}
	if f.actor != "" {
		meta["actor"] = f.actor
	}
	if len(f.actions) > 0 {
		meta["actions"] = f.actions
	}
	if f.target != "" {
		meta["target"] = f.target
	}
	if f.targetType != "" {
		meta["targetType"] = f.targetType
	}
	if f.targetID != "" {
		meta["targetId"] = f.targetID
	}
	if f.query != "" {
		meta["q"] = f.query
	}
	targetType, targetID := "platform", "audit"
	if auditOrg != nil {
		targetType, targetID = "organization", *auditOrg
	}
	s.audit(r.Context(), r, p, auditOrg, "audit.export", targetType, targetID, meta)
}

func neutralizeCSVCell(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + s
	}
	trimmed := strings.TrimLeft(s, " \t\r\n")
	if trimmed != "" {
		switch trimmed[0] {
		case '=', '+', '-', '@':
			return "'" + s
		}
	}
	return s
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func writeAuditEventsCSV(w io.Writer, next func() (*auditEvent, error)) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "created_at", "org_id", "actor_user_id", "actor_email", "action", "target_type", "target_id", "ip", "metadata"}); err != nil {
		return err
	}
	for {
		ev, err := next()
		if err != nil {
			return err
		}
		if ev == nil {
			break
		}
		metaBytes, _ := json.Marshal(ev.Metadata)
		row := []string{
			strconv.FormatInt(ev.ID, 10),
			ev.CreatedAt.UTC().Format(time.RFC3339Nano),
			neutralizeCSVCell(derefString(ev.OrgID)),
			neutralizeCSVCell(derefString(ev.ActorUserID)),
			neutralizeCSVCell(derefString(ev.ActorEmail)),
			neutralizeCSVCell(ev.Action),
			neutralizeCSVCell(ev.TargetType),
			neutralizeCSVCell(ev.TargetID),
			neutralizeCSVCell(ev.IP),
			neutralizeCSVCell(string(metaBytes)),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeAuditEventsJSON(w io.Writer, next func() (*auditEvent, error)) error {
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	first := true
	for {
		ev, err := next()
		if err != nil {
			return err
		}
		if ev == nil {
			break
		}
		if !first {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]\n")
	return err
}

func (s *Server) getAuditRetention(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgPerm(w, r, p, permAuditRead)
	if !ok {
		return
	}
	var days *int
	err := s.db.QueryRow(r.Context(), `SELECT audit_retention_days FROM organizations WHERE id = $1`, orgID).Scan(&days)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, makeAuditRetention(days, s.cfg.AuditRetentionDays))
}

func (s *Server) updateAuditRetention(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.orgFromPath(w, r, p, true)
	if !ok {
		return
	}
	var in struct {
		RetentionDays *int `json:"retentionDays"`
	}
	if !decode(w, r, 4<<10, &in) {
		return
	}
	if in.RetentionDays != nil && (*in.RetentionDays < 0 || *in.RetentionDays > config.MaxAuditRetentionDays) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_retention", fmt.Sprintf("Retention days must be null, 0 (keep forever), or between 1 and %d.", config.MaxAuditRetentionDays))
		return
	}
	var before *int
	err := s.db.QueryRow(r.Context(), `SELECT audit_retention_days FROM organizations WHERE id = $1`, orgID).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE organizations SET audit_retention_days = $2 WHERE id = $1`, orgID, in.RetentionDays); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, &orgID, "audit.retention.update", "organization", orgID, map[string]any{"before": before, "after": in.RetentionDays})
	writeJSON(w, http.StatusOK, makeAuditRetention(in.RetentionDays, s.cfg.AuditRetentionDays))
}

func (s *Server) purgeExpiredAuditEvents(ctx context.Context) {
	defaultDays := s.cfg.AuditRetentionDays
	var total int64
	for ctx.Err() == nil {
		deleted, locked, err := s.purgeAuditBatch(ctx, `
			DELETE FROM audit_events
			WHERE id IN (
				SELECT e.id
				FROM audit_events e
				JOIN organizations o ON o.id = e.org_id
				WHERE coalesce(o.audit_retention_days, $1) > 0
				  AND e.created_at < now() - make_interval(days => coalesce(o.audit_retention_days, $1))
				ORDER BY e.id
				LIMIT $2
			)`, defaultDays, auditPurgeBatchSize)
		if err != nil {
			s.log.Error("purging organization audit events failed", "err", err)
			return
		}
		if !locked {
			return
		}
		total += deleted
		if deleted < auditPurgeBatchSize {
			break
		}
	}
	if defaultDays > 0 {
		for ctx.Err() == nil {
			deleted, locked, err := s.purgeAuditBatch(ctx, `
				DELETE FROM audit_events
				WHERE id IN (
					SELECT id
					FROM audit_events
					WHERE org_id IS NULL
					  AND created_at < now() - make_interval(days => $1)
					ORDER BY id
					LIMIT $2
				)`, defaultDays, auditPurgeBatchSize)
			if err != nil {
				s.log.Error("purging platform audit events failed", "err", err)
				return
			}
			if !locked {
				return
			}
			total += deleted
			if deleted < auditPurgeBatchSize {
				break
			}
		}
	}
	if total > 0 {
		s.log.Info("purged expired audit events", "deleted", total)
	}
}

func (s *Server) purgeAuditBatch(ctx context.Context, query string, args ...any) (int64, bool, error) {
	var deleted int64
	var locked bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, auditPurgeLockID).Scan(&locked); err != nil || !locked {
			return err
		}
		tag, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected()
		return nil
	})
	return deleted, locked, err
}

func (s *Server) mailboxOrgForAccount(ctx context.Context, userID, email string) *string {
	var orgID string
	var err error
	if userID != "" {
		err = s.db.QueryRow(ctx, `
			SELECT m.org_id
			FROM mailboxes m
			LEFT JOIN users u ON u.id = $1
			WHERE m.owner_user_id = $1 OR (u.email IS NOT NULL AND m.address = u.email)
			ORDER BY (u.email IS NOT NULL AND m.address = u.email) DESC, m.active DESC, m.created_at ASC
			LIMIT 1`, userID).Scan(&orgID)
	} else if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
		err = s.db.QueryRow(ctx, `SELECT org_id FROM mailboxes WHERE address = $1 LIMIT 1`, email).Scan(&orgID)
	} else {
		return nil
	}
	if err != nil || orgID == "" {
		return nil
	}
	return &orgID
}

func (s *Server) mailboxOrgForOIDC(ctx context.Context, providerID, subject, email string, emailVerified bool) *string {
	var userID string
	if providerID != "" && subject != "" {
		if err := s.db.QueryRow(ctx, `SELECT user_id FROM user_identities WHERE provider_id = $1 AND subject = $2`, providerID, subject).Scan(&userID); err == nil && userID != "" {
			return s.mailboxOrgForAccount(ctx, userID, email)
		}
	}
	if emailVerified && email != "" {
		return s.mailboxOrgForAccount(ctx, "", email)
	}
	return nil
}

package api

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/scalastudios/aether/internal/mailtrace"
)

const (
	tracePage         = 50
	traceExport       = 50000
	traceScope        = `WITH scope AS (SELECT coalesce(array_agg(name), '{}') AS names FROM domains WHERE org_id = $1::uuid)`
	traceVisibleEvent = `(e.recipient = '' OR e.recipient = ANY(v.recipients) OR e.orig_recipient = ANY(v.recipients))`
)

var traceFrom = `
		FROM mail_trace_messages m CROSS JOIN scope
		CROSS JOIN LATERAL (SELECT $1::uuid IS NULL OR m.origin_domain = ANY(scope.names) AS whole) f
		CROSS JOIN LATERAL (SELECT CASE WHEN f.whole THEN m.recipients
			ELSE ARRAY(SELECT a FROM unnest(m.recipients) a WHERE substring(a from '@([^@]+)$') = ANY(scope.names)) END AS recipients) v
		CROSS JOIN LATERAL (SELECT CASE WHEN f.whole THEN m.status ELSE coalesce((
			SELECT s.value FROM jsonb_each_text(m.recipient_status) s WHERE s.key = ANY(v.recipients)
			ORDER BY array_position('{` + strings.Join(mailtrace.Severity, ",") + `}'::text[], s.value) LIMIT 1), m.status) END AS status) st`

type traceFilter struct {
	sender, recipient, subject, messageID, status string
	from, to                                      *time.Time
}

type traceSummary struct {
	ID         int64     `json:"id"`
	ReceivedAt time.Time `json:"receivedAt"`
	Status     string    `json:"status"`
	Sender     string    `json:"sender"`
	Recipients []string  `json:"recipients"`
	Subject    string    `json:"subject"`
	MessageID  string    `json:"messageId"`
	QueueID    *string   `json:"queueId"`
	Service    string    `json:"service"`
	ClientIP   string    `json:"clientIp"`
	TLS        *string   `json:"tls"`
	SpamScore  *float32  `json:"spamScore"`
	Spam       bool      `json:"spam"`
	LastEvent  *string   `json:"lastEvent"`
	Detail     *string   `json:"detail"`
}

func (s *Server) traceOrg(w http.ResponseWriter, r *http.Request, p *principal) (*string, bool) {
	if r.PathValue("orgId") == "" {
		if p.apiKey != nil || !p.User.IsPlatformAdmin {
			forbidden(w)
			return nil, false
		}
		return nil, true
	}
	orgID, ok := s.orgFromPath(w, r, p, true)
	return &orgID, ok
}

func parseTraceFilter(w http.ResponseWriter, r *http.Request) (traceFilter, bool) {
	q := r.URL.Query()
	f := traceFilter{
		sender:    strings.ToLower(strings.TrimSpace(q.Get("sender"))),
		recipient: strings.ToLower(strings.TrimSpace(q.Get("recipient"))),
		subject:   strings.TrimSpace(q.Get("subject")),
		messageID: strings.Trim(strings.TrimSpace(q.Get("messageId")), "<>"),
		status:    q.Get("status"),
	}
	for _, v := range []string{f.sender, f.recipient, f.subject, f.messageID} {
		if len(v) > 998 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_filter", "Search terms can be up to 998 characters.")
			return f, false
		}
	}
	if f.status != "" && !slices.Contains(mailtrace.Severity, f.status) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_filter", "Status must be one of "+strings.Join(mailtrace.Severity, ", ")+".")
		return f, false
	}
	for name, dst := range map[string]**time.Time{"from": &f.from, "to": &f.to} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, "invalid_filter", name+" must be an RFC 3339 timestamp, for example 2026-11-22T09:00:00Z.")
				return f, false
			}
			*dst = &t
		}
	}
	return f, true
}

func traceLike(term string) string {
	if term == "" {
		return ""
	}
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term) + "%"
}

func (s *Server) queryTrace(r *http.Request, orgID *string, f traceFilter, before int64, limit int) ([]traceSummary, error) {
	rows, err := s.db.Query(r.Context(), traceScope+`
		SELECT m.id, m.received_at, st.status, m.sender, v.recipients, m.subject, m.message_id, m.queue_id, m.service, m.client_ip, m.tls, m.spam_score, m.spam, last.kind, last.detail`+traceFrom+`
		LEFT JOIN LATERAL (SELECT e.kind, e.detail FROM mail_trace_events e WHERE e.trace_id = m.id AND e.kind <> 'notified' AND `+traceVisibleEvent+` ORDER BY e.id DESC LIMIT 1) last ON true
		WHERE ($1::uuid IS NULL OR m.domains && scope.names)
			AND m.id < $2
			AND ($3 = '' OR m.sender LIKE $3)
			AND ($4 = '' OR EXISTS (SELECT 1 FROM unnest(v.recipients) a WHERE a LIKE $4))
			AND ($5 = '' OR m.subject ILIKE $5)
			AND ($6 = '' OR m.message_id = $6)
			AND ($7::timestamptz IS NULL OR m.received_at >= $7)
			AND ($8::timestamptz IS NULL OR m.received_at < $8)
			AND ($9 = '' OR st.status = $9)
		ORDER BY m.id DESC LIMIT $10`,
		orgID, before, traceLike(f.sender), traceLike(f.recipient), traceLike(f.subject), f.messageID, f.from, f.to, f.status, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[traceSummary])
}

func (s *Server) listMessageTrace(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.traceOrg(w, r, p)
	if !ok {
		return
	}
	f, ok := parseTraceFilter(w, r)
	if !ok {
		return
	}
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil || before <= 0 {
		before = 1<<63 - 1
	}
	messages, err := s.queryTrace(r, orgID, f, before, tracePage)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := struct {
		Messages      []traceSummary `json:"messages"`
		RetentionDays *int           `json:"retentionDays"`
		ReadAt        *time.Time     `json:"readAt"`
	}{Messages: nonNil(messages)}
	err = s.db.QueryRow(r.Context(), `SELECT retention_days, read_at FROM mail_trace_sources ORDER BY read_at DESC LIMIT 1`).Scan(&out.RetentionDays, &out.ReadAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getMessageTrace(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.traceOrg(w, r, p)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w)
		return
	}
	type event struct {
		At            time.Time `json:"at"`
		Kind          string    `json:"kind"`
		Recipient     string    `json:"recipient"`
		OrigRecipient string    `json:"origRecipient"`
		Relay         string    `json:"relay"`
		DSN           string    `json:"dsn"`
		Detail        string    `json:"detail"`
		TLS           string    `json:"tls"`
	}
	type related struct {
		ID         int64     `json:"id"`
		ReceivedAt time.Time `json:"receivedAt"`
		Service    string    `json:"service"`
		Sender     string    `json:"sender"`
		Status     string    `json:"status"`
	}
	var out struct {
		traceSummary
		ClientName      string            `json:"clientName"`
		SASLUsername    string            `json:"saslUsername"`
		Size            *int64            `json:"size"`
		RecipientStatus map[string]string `json:"recipientStatus"`
		Events          []event           `json:"events"`
		Related         []related         `json:"related"`
	}
	m := &out.traceSummary
	err = s.db.QueryRow(r.Context(), traceScope+`
		SELECT m.id, m.received_at, st.status, m.sender, v.recipients, m.subject, m.message_id, m.queue_id, m.service, m.client_ip, m.tls, m.spam_score, m.spam,
			m.client_name, m.sasl_username, m.size,
			coalesce((SELECT jsonb_object_agg(s.key, s.value) FROM jsonb_each_text(m.recipient_status) s WHERE s.key = ANY(v.recipients)), '{}')`+traceFrom+`
		WHERE m.id = $2 AND ($1::uuid IS NULL OR m.domains && scope.names)`, orgID, id).
		Scan(&m.ID, &m.ReceivedAt, &m.Status, &m.Sender, &m.Recipients, &m.Subject, &m.MessageID, &m.QueueID, &m.Service, &m.ClientIP, &m.TLS, &m.SpamScore, &m.Spam,
			&out.ClientName, &out.SASLUsername, &out.Size, &out.RecipientStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT e.at, e.kind, e.recipient, e.orig_recipient, e.relay, e.dsn, e.detail, e.tls
		FROM mail_trace_events e CROSS JOIN (SELECT $2::text[] AS recipients) v
		WHERE e.trace_id = $1 AND `+traceVisibleEvent+` ORDER BY e.id`, id, m.Recipients)
	if err == nil {
		out.Events, err = pgx.CollectRows(rows, pgx.RowToStructByPos[event])
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err = s.db.Query(r.Context(), traceScope+`
		SELECT m.id, m.received_at, m.service, m.sender, st.status`+traceFrom+`
		WHERE m.message_id = $2 AND m.message_id <> '' AND m.id <> $3 AND ($1::uuid IS NULL OR m.domains && scope.names)
		ORDER BY m.id LIMIT 20`, orgID, m.MessageID, id)
	if err == nil {
		out.Related, err = pgx.CollectRows(rows, pgx.RowToStructByPos[related])
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out.Events, out.Related = nonNil(out.Events), nonNil(out.Related)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) exportMessageTrace(w http.ResponseWriter, r *http.Request, p *principal) {
	orgID, ok := s.traceOrg(w, r, p)
	if !ok {
		return
	}
	f, ok := parseTraceFilter(w, r)
	if !ok {
		return
	}
	messages, err := s.queryTrace(r, orgID, f, 1<<63-1, traceExport)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r.Context(), r, p, orgID, "message_trace.export", "message_trace", "", map[string]any{
		"rows": len(messages), "sender": f.sender, "recipient": f.recipient, "subject": f.subject, "messageId": f.messageID, "status": f.status, "from": f.from, "to": f.to,
	})
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="message-trace-%s.csv"`, time.Now().UTC().Format("20060102-1504")))
	out := csv.NewWriter(w)
	out.Write([]string{"Received (UTC)", "Status", "Sender", "Recipients", "Subject", "Message-ID", "Queue ID", "Client IP", "TLS", "Spam score", "Last event", "Details"})
	for _, m := range messages {
		score := ""
		if m.SpamScore != nil {
			score = strconv.FormatFloat(float64(*m.SpamScore), 'f', 2, 32)
		}
		out.Write(traceCSVRow(m.ReceivedAt.UTC().Format(time.RFC3339), m.Status, m.Sender, strings.Join(m.Recipients, "; "), m.Subject, m.MessageID,
			traceText(m.QueueID), m.ClientIP, traceText(m.TLS), score, traceText(m.LastEvent), traceText(m.Detail)))
	}
	out.Flush()
}

func traceCSVRow(cells ...string) []string {
	for i, c := range cells {
		if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
			cells[i] = "'" + c
		}
	}
	return cells
}

func traceText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

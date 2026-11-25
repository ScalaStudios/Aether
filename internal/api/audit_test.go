package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseAuditFilter(t *testing.T) {
	q := url.Values{
		"actor":  {" alice@example.com "},
		"action": {"auth.login,auth.*", "auth.login", "mailbox.create"},
		"since":  {"2026-01-01"},
		"until":  {"2026-01-31"},
		"limit":  {"5000"},
		"before": {"42"},
		"q":      {"203.0.113.7"},
	}
	f, code, _ := parseAuditFilter(q, 50, 200)
	if code != "" {
		t.Fatalf("unexpected error code %q", code)
	}
	if f.actor != "alice@example.com" || f.query != "203.0.113.7" || f.before != 42 || f.limit != 200 {
		t.Fatalf("unexpected filter %+v", f)
	}
	if strings.Join(f.actions, " ") != "auth.login auth.* mailbox.create" {
		t.Fatalf("actions = %v", f.actions)
	}
	if !f.since.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || !f.until.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) || !f.untilExcl {
		t.Fatalf("date range = %v %v %v", f.since, f.until, f.untilExcl)
	}

	f, code, _ = parseAuditFilter(url.Values{"until": {"2026-01-31T10:00:00Z"}}, 50, 200)
	if code != "" || f.untilExcl || !f.until.Equal(time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)) || f.limit != 50 {
		t.Fatalf("timestamp until = %+v %q", f, code)
	}

	for _, bad := range []url.Values{
		{"since": {"yesterday"}},
		{"until": {"31/01/2026"}},
		{"since": {"2026-02-01"}, "until": {"2026-01-31"}},
		{"since": {"2026-02-01T00:00:00Z"}, "until": {"2026-01-31T23:59:59Z"}},
	} {
		if _, code, _ := parseAuditFilter(bad, 50, 200); code != "invalid_date" {
			t.Errorf("%v: code = %q, want invalid_date", bad, code)
		}
	}
	if _, code, _ := parseAuditFilter(url.Values{"since": {"2026-01-31"}, "until": {"2026-01-31"}}, 50, 200); code != "" {
		t.Errorf("single-day range rejected: %q", code)
	}

	many := make([]string, 40)
	for i := range many {
		many[i] = "a" + strings.Repeat("x", i)
	}
	f, _, _ = parseAuditFilter(url.Values{"action": {strings.Join(many, ",")}}, 50, 200)
	if len(f.actions) != maxAuditActionFilters {
		t.Errorf("actions capped at %d, want %d", len(f.actions), maxAuditActionFilters)
	}
}

func TestBuildAuditQueryPlaceholders(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		f    auditFilter
		want []string
	}{
		{"org scope", auditFilter{orgScope: "org", orgID: "o", limit: 50}, []string{"e.org_id = $1", "LIMIT $2"}},
		{"no org", auditFilter{orgScope: "none", limit: 50}, []string{"e.org_id IS NULL", "LIMIT $1"}},
		{"actor uuid", auditFilter{actor: "0f8fad5b-d9cb-469f-a165-70867728950e", limit: 1}, []string{"e.actor_user_id = $1 OR e.metadata->>'apiKeyId' = $2", "LIMIT $3"}},
		{"actor email", auditFilter{actor: "50%_off@x", limit: 1}, []string{"ILIKE $1", "= $2", "LIMIT $3"}},
		{"actions", auditFilter{actions: []string{"auth.*", "mailbox.create"}, limit: 1}, []string{"e.action LIKE $1", "e.action = ANY($2)", "LIMIT $3"}},
		{"everything", auditFilter{orgScope: "org", orgID: "o", before: 9, targetType: "user", targetID: "u", target: "t", since: &since, until: &since, untilExcl: true, query: "x", limit: 2},
			[]string{"e.org_id = $1", "e.id < $2", "e.target_type = $3", "e.target_id = $4", "ILIKE $5", "e.created_at >= $6", "e.created_at < $7", "websearch_to_tsquery('simple', $8)", "ILIKE $9", "LIMIT $10"}},
	}
	for _, tc := range cases {
		sql, args := buildAuditQuery(tc.f)
		for _, w := range tc.want {
			if !strings.Contains(sql, w) {
				t.Errorf("%s: %q missing from %s", tc.name, w, sql)
			}
		}
		if !strings.Contains(sql, "$"+strconv.Itoa(len(args))) || strings.Contains(sql, "$"+strconv.Itoa(len(args)+1)) {
			t.Errorf("%s: %d args do not match placeholders in %s", tc.name, len(args), sql)
		}
		if strings.Contains(tc.name, "actor email") && args[0] != `%50\%\_off@x%` {
			t.Errorf("LIKE pattern not escaped: %v", args[0])
		}
	}
	if sql, _ := buildAuditQuery(auditFilter{orgScope: "all", limit: 1}); strings.Contains(sql, "WHERE") {
		t.Errorf("unscoped query has a WHERE clause: %s", sql)
	}
}

func auditEventsIter(events []auditEvent) func() (*auditEvent, error) {
	i := 0
	return func() (*auditEvent, error) {
		if i >= len(events) {
			return nil, nil
		}
		i++
		return &events[i-1], nil
	}
}

func TestAuditExportWriters(t *testing.T) {
	org, actor, email := "org-1", "user-1", "=cmd@example.com"
	events := []auditEvent{
		{ID: 2, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), OrgID: &org, ActorUserID: &actor, ActorEmail: &email, Action: "auth.login", TargetType: "user", TargetID: "+1", IP: "203.0.113.7", Metadata: map[string]any{"method": "password"}},
		{ID: 1, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Action: "auth.login.failed", TargetType: "user", TargetID: " -x", Metadata: map[string]any{}},
	}

	var buf bytes.Buffer
	if err := writeAuditEventsCSV(&buf, auditEventsIter(events)); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0][0] != "id" || rows[1][0] != "2" || rows[2][0] != "1" {
		t.Fatalf("csv rows = %v", rows)
	}
	if rows[1][4] != "'=cmd@example.com" || rows[1][7] != "'+1" || rows[2][7] != "' -x" || rows[2][2] != "" {
		t.Fatalf("csv cells not neutralized: %v", rows)
	}
	if rows[1][9] != `{"method":"password"}` || rows[1][1] != "2026-01-02T03:04:05Z" {
		t.Fatalf("csv metadata or time = %q %q", rows[1][9], rows[1][1])
	}

	buf.Reset()
	if err := writeAuditEventsJSON(&buf, auditEventsIter(events)); err != nil {
		t.Fatal(err)
	}
	var decoded []auditEvent
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("json export does not parse: %v: %s", err, buf.String())
	}
	if len(decoded) != 2 || decoded[0].ID != 2 || *decoded[0].ActorEmail != email || decoded[1].OrgID != nil {
		t.Fatalf("json export = %+v", decoded)
	}

	buf.Reset()
	if err := writeAuditEventsJSON(&buf, auditEventsIter(nil)); err != nil || strings.TrimSpace(buf.String()) != "[]" {
		t.Fatalf("empty json export = %q %v", buf.String(), err)
	}
}

func TestMakeAuditRetention(t *testing.T) {
	if r := makeAuditRetention(nil, 0); r.RetentionDays != nil || r.EffectiveRetentionDays != 0 {
		t.Errorf("default = %+v", r)
	}
	days := 30
	if r := makeAuditRetention(&days, 365); r.EffectiveRetentionDays != 30 || r.DefaultRetentionDays != 365 {
		t.Errorf("override = %+v", r)
	}
	zero := 0
	if r := makeAuditRetention(&zero, 365); r.EffectiveRetentionDays != 0 {
		t.Errorf("keep forever = %+v", r)
	}
}

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/scalastudios/aether/internal/config"
)

func TestAlertRuleBreached(t *testing.T) {
	gte := alertRule{Comparator: "gte", Threshold: 50}
	if gte.breached(49.9) || !gte.breached(50) || !gte.breached(51) {
		t.Fatal("gte comparator behaved unexpectedly")
	}
	lte := alertRule{Comparator: "lte", Threshold: 14}
	if !lte.breached(13.9) || !lte.breached(14) || lte.breached(14.1) {
		t.Fatal("lte comparator behaved unexpectedly")
	}
}

func TestDecideAlertTransition(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	cooldown := 15 * time.Minute
	recent := now.Add(-5 * time.Minute)
	old := now.Add(-20 * time.Minute)

	cases := []struct {
		name         string
		activeStatus string
		lastNotified *time.Time
		breached     bool
		wantAction   string
		wantNotify   bool
		wantEvent    string
	}{
		{"new_fire_notifies", "", nil, true, "fire", true, "alert.firing"},
		{"flapping_within_cooldown_suppresses_notify", "", &recent, true, "fire", false, "alert.firing"},
		{"flapping_after_cooldown_notifies", "", &old, true, "fire", true, "alert.firing"},
		{"firing_within_cooldown_deduplicates", "firing", &recent, true, "update", false, "alert.firing"},
		{"firing_after_cooldown_renotifies", "firing", &old, true, "update", true, "alert.firing"},
		{"firing_never_notified_retries", "firing", nil, true, "update", true, "alert.firing"},
		{"acknowledged_suppresses_renotify", "acknowledged", &old, true, "update", false, ""},
		{"firing_resolves_and_notifies", "firing", &recent, false, "resolve", true, "alert.resolved"},
		{"acknowledged_resolves_and_notifies", "acknowledged", &recent, false, "resolve", true, "alert.resolved"},
		{"healthy_idle_noop", "", nil, false, "noop", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideAlertTransition(tc.activeStatus, tc.lastNotified, tc.breached, cooldown, now)
			if got.action != tc.wantAction || got.notify != tc.wantNotify || got.event != tc.wantEvent {
				t.Fatalf("got %+v, want action=%s notify=%v event=%s", got, tc.wantAction, tc.wantNotify, tc.wantEvent)
			}
		})
	}
}

func TestValidateAlertRuleUpdate(t *testing.T) {
	base := alertRule{
		Key:             "disk_vmail_usage",
		Severity:        "critical",
		Enabled:         true,
		Threshold:       85,
		Unit:            "percent",
		Comparator:      "gte",
		CooldownSeconds: 3600,
		NotifyEmail:     true,
		NotifyWebhook:   true,
		EmailRecipients: []string{},
	}

	enabled := false
	sev := "WARNING"
	thresh := 90.0
	cd := 1800
	rcpts := []string{"OPS@Example.com", "admin@example.com", "ops@example.com", " "}
	next, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{
		Enabled:         &enabled,
		Severity:        &sev,
		Threshold:       &thresh,
		CooldownSeconds: &cd,
		EmailRecipients: &rcpts,
	})
	if code != "" {
		t.Fatalf("unexpected validation error: %s", code)
	}
	if next.Enabled || next.Severity != "warning" || next.Threshold != 90 || next.CooldownSeconds != 1800 {
		t.Fatalf("unexpected updated rule: %+v", next)
	}
	if len(next.EmailRecipients) != 2 || next.EmailRecipients[0] != "admin@example.com" || next.EmailRecipients[1] != "ops@example.com" {
		t.Fatalf("unexpected normalized recipients: %+v", next.EmailRecipients)
	}

	badSev := "urgent"
	if _, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{Severity: &badSev}); code != "invalid_severity" {
		t.Fatalf("got %q, want invalid_severity", code)
	}
	overPct := 101.0
	if _, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{Threshold: &overPct}); code != "invalid_threshold" {
		t.Fatalf("got %q, want invalid_threshold", code)
	}
	neg := -1.0
	if _, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{Threshold: &neg}); code != "invalid_threshold" {
		t.Fatalf("got %q, want invalid_threshold", code)
	}
	nan := math.NaN()
	if _, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{Threshold: &nan}); code != "invalid_threshold" {
		t.Fatalf("got %q, want invalid_threshold", code)
	}
	tooShort := 30
	if _, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{CooldownSeconds: &tooShort}); code != "invalid_cooldown" {
		t.Fatalf("got %q, want invalid_cooldown", code)
	}
	tooLong := 700000
	if _, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{CooldownSeconds: &tooLong}); code != "invalid_cooldown" {
		t.Fatalf("got %q, want invalid_cooldown", code)
	}
	badRcpt := []string{"not-an-email"}
	if _, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{EmailRecipients: &badRcpt}); code != "invalid_recipients" {
		t.Fatalf("got %q, want invalid_recipients", code)
	}
	displayRcpt := []string{"Admin <admin@example.com>"}
	if _, code, _ := validateAlertRuleUpdate(base, alertRuleUpdateInput{EmailRecipients: &displayRcpt}); code != "invalid_recipients" {
		t.Fatalf("got %q, want invalid_recipients", code)
	}
}

func parseFamilies(t *testing.T, text string) map[string]*dto.MetricFamily {
	t.Helper()
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(text))
	if err != nil {
		t.Fatalf("parse metrics: %v", err)
	}
	return families
}

func TestPostfixAndRspamdExtractors(t *testing.T) {
	families := parseFamilies(t, `# TYPE postfix_showq_queue_depth gauge
postfix_showq_queue_depth{queue="active"} 4
postfix_showq_queue_depth{queue="deferred"} 16
# TYPE postfix_showq_message_age_seconds histogram
postfix_showq_message_age_seconds_bucket{queue="deferred",le="10"} 2
postfix_showq_message_age_seconds_bucket{queue="deferred",le="100"} 10
postfix_showq_message_age_seconds_bucket{queue="deferred",le="1000"} 16
postfix_showq_message_age_seconds_bucket{queue="deferred",le="10000"} 16
postfix_showq_message_age_seconds_bucket{queue="deferred",le="+Inf"} 16
postfix_showq_message_age_seconds_sum{queue="deferred"} 4400
postfix_showq_message_age_seconds_count{queue="deferred"} 16
# TYPE postfix_smtp_messages_processed_total counter
postfix_smtp_messages_processed_total{status="sent"} 120
postfix_smtp_messages_processed_total{status="deferred"} 7
postfix_smtp_messages_processed_total{status="bounced"} 5
`)
	snap, failTotal, hasFail := extractPostfixExporterSnapshot(families)
	if snap == nil || snap.total != 20 || snap.byQueue["active"] != 4 || snap.byQueue["deferred"] != 16 {
		t.Fatalf("unexpected postfix snapshot: %+v", snap)
	}
	if !snap.hasAge || snap.oldestSeconds != 100 {
		t.Fatalf("oldestSeconds = %v, want the lower bound 100", snap.oldestSeconds)
	}
	if !hasFail || failTotal != 12 {
		t.Fatalf("failTotal = %v (hasFail=%v), want 12", failTotal, hasFail)
	}
	if snap, _, _ := extractPostfixExporterSnapshot(parseFamilies(t, "up 1\n")); snap != nil {
		t.Fatalf("snapshot without queue metrics = %+v, want nil", snap)
	}

	rl, ok := extractRspamdRateLimits(parseFamilies(t, `rspamd_actions_total{type="no action"} 500
rspamd_actions_total{type="soft reject"} 3
rspamd_actions_total{type="greylist"} 12
`))
	if !ok || rl != 3 {
		t.Fatalf("rspamd soft reject = %v (ok=%v), want 3", rl, ok)
	}
}

func TestReadClamAVSignatureDate(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		got <- string(buf[:n])
		conn.Write([]byte("ClamAV 1.4.3/27831/Mon Nov  3 08:24:01 2026\x00"))
	}()
	built, version, err := readClamAVSignatureDate(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatalf("readClamAVSignatureDate: %v", err)
	}
	if cmd := <-got; cmd != "zVERSION\x00" {
		t.Fatalf("sent %q, want zVERSION", cmd)
	}
	if version != "ClamAV 1.4.3/27831" || !built.Equal(time.Date(2026, 11, 3, 8, 24, 1, 0, time.UTC)) {
		t.Fatalf("got %q %v", version, built)
	}
}

func TestCounterWindow(t *testing.T) {
	var cw counterWindow
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if v := cw.Observe(100, t0, 15*time.Minute); v != 0 {
		t.Fatalf("initial observe = %v, want 0", v)
	}
	if v := cw.Observe(106, t0.Add(2*time.Minute), 15*time.Minute); v != 6 {
		t.Fatalf("delta = %v, want 6", v)
	}
	if v := cw.Observe(4, t0.Add(5*time.Minute), 15*time.Minute); v != 10 {
		t.Fatalf("counter reset delta = %v, want 10", v)
	}
	if v := cw.Observe(4, t0.Add(18*time.Minute), 15*time.Minute); v != 4 {
		t.Fatalf("window expiry = %v, want 4", v)
	}
}

func writeTestCert(t *testing.T, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mail.example.com"},
		NotBefore:    notAfter.Add(-30 * 24 * time.Hour),
		NotAfter:     notAfter,
		DNSNames:     []string{"mail.example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fullchain.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadCertExpiry(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	soonPath := writeTestCert(t, now.Add(7*24*time.Hour))
	info, err := readCertExpiry(soonPath, "mail.example.com", now)
	if err != nil {
		t.Fatalf("readCertExpiry failed: %v", err)
	}
	if math.Abs(info.daysRemaining-7) > 0.01 || info.subject != "mail.example.com" {
		t.Fatalf("unexpected cert info: %+v", info)
	}

	expiredPath := writeTestCert(t, now.Add(-48*time.Hour))
	expired, err := readCertExpiry(expiredPath, "mail.example.com", now)
	if err != nil {
		t.Fatalf("readCertExpiry expired failed: %v", err)
	}
	if expired.daysRemaining >= 0 {
		t.Fatalf("expected negative days remaining for expired cert, got %v", expired.daysRemaining)
	}

	badPath := filepath.Join(t.TempDir(), "empty.pem")
	os.WriteFile(badPath, []byte("not a cert"), 0600)
	if _, err := readCertExpiry(badPath, "mail.example.com", now); err == nil {
		t.Fatal("expected error for invalid PEM")
	}
	if _, err := readCertExpiry(soonPath, "other.example.com", now); err == nil {
		t.Fatal("expected error when no certificate matches the mail hostname")
	}
}

func TestDiskUsageStatfs(t *testing.T) {
	info, err := diskUsageStatfs(t.TempDir())
	if err != nil {
		t.Fatalf("diskUsageStatfs failed: %v", err)
	}
	if info.totalBytes == 0 || info.percent < 0 || info.percent > 100 {
		t.Fatalf("unexpected disk info: %+v", info)
	}
	if _, err := diskUsageStatfs(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestEvaluateDomainDNSRecords(t *testing.T) {
	pubURL, _ := url.Parse("https://mail.example.com")
	d := &domain{
		ID:           "00000000-0000-0000-0000-000000000001",
		OrgID:        "00000000-0000-0000-0000-000000000002",
		Name:         "example.com",
		DKIMSelector: "aether202611",
		DKIMRecord:   "v=DKIM1; k=rsa; p=PUBKEY123",
	}
	keys := []dkimKey{{Selector: "aether202611", State: "active", DNSName: "aether202611._domainkey.example.com", Record: "v=DKIM1; k=rsa; p=PUBKEY123"}}

	okServer := &Server{
		cfg: &config.Config{MailHostname: "mail.example.com", PublicURL: pubURL, MTASTSMode: "testing"},
		resolve: fakeDNS{
			hosts: map[string][]string{"mail.example.com": {"198.51.100.10"}},
			mx:    map[string][]*net.MX{"example.com": {{Host: "mail.example.com.", Pref: 10}}},
			txt: map[string][]string{
				"example.com":                         {"v=spf1 ip4:198.51.100.10 -all"},
				"aether202611._domainkey.example.com": {"v=DKIM1; k=rsa; p=PUBKEY123"},
				"_dmarc.example.com":                  {"v=DMARC1; p=quarantine;"},
			},
		},
	}
	if failed, ok := okServer.evaluateDomainDNSRecords(context.Background(), d, keys); !ok || len(failed) != 0 {
		t.Fatalf("expected 0 failed DNS records, got %+v (ok=%v)", failed, ok)
	}

	brokenServer := &Server{
		cfg: &config.Config{MailHostname: "mail.example.com", PublicURL: pubURL, MTASTSMode: "testing"},
		resolve: fakeDNS{
			hosts: map[string][]string{"mail.example.com": {"198.51.100.10"}},
			mx:    map[string][]*net.MX{"example.com": {{Host: "other.example.net.", Pref: 10}}},
			txt: map[string][]string{
				"example.com": {"v=spf1 ip4:198.51.100.10 ~all"},
			},
		},
	}
	failed, ok := brokenServer.evaluateDomainDNSRecords(context.Background(), d, keys)
	if !ok || len(failed) != 3 {
		t.Fatalf("expected 3 failed DNS records (mx, dkim, dmarc; spf softfail is warning), got %d: %+v", len(failed), failed)
	}

	brokenServer.resolve = fakeDNS{txt: map[string][]string{"example.com": {"v=spf1 mx -all"}}}
	if failed, ok := brokenServer.evaluateDomainDNSRecords(context.Background(), d, keys); ok {
		t.Fatalf("a resolver error must not produce a sample, got %+v", failed)
	}
}

func TestFormatAlertEmailAndWebhookPayload(t *testing.T) {
	now := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	val := 72.0
	thresh := 50.0
	item := alertItem{
		ID:             "11111111-1111-1111-1111-111111111111",
		RuleKey:        "mail_queue_size",
		RuleName:       "Mail queue size",
		Category:       "mail",
		Scope:          "platform",
		Severity:       "warning",
		Status:         "firing",
		TargetType:     "queue",
		TargetID:       "postfix",
		TargetLabel:    "Postfix mail queue",
		Title:          "Mail queue size exceeded threshold",
		Message:        "Postfix mail queue currently holds 72 messages (threshold: 50 messages).",
		CurrentValue:   &val,
		ThresholdValue: &thresh,
		Unit:           "messages",
		Details:        map[string]any{"active": 72},
		Occurrences:    2,
		FiredAt:        now,
		LastSeenAt:     now,
	}

	subj, body := formatAlertEmail("alert.firing", item, "https://mail.example.com")
	if !strings.Contains(subj, "[Aether Alert][WARNING]") || !strings.Contains(body, "Observed: 72 messages") {
		t.Fatalf("unexpected firing email: %q / %q", subj, body)
	}

	resAt := now.Add(10 * time.Minute)
	item.Status = "resolved"
	item.ResolvedAt = &resAt
	resSubj, resBody := formatAlertEmail("alert.resolved", item, "https://mail.example.com")
	if !strings.Contains(resSubj, "[Aether Resolved]") || !strings.Contains(resBody, "Resolved at:") {
		t.Fatalf("unexpected resolved email: %q / %q", resSubj, resBody)
	}

	payload := buildAlertWebhookPayload("alert.firing", item, now)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal webhook payload: %v", err)
	}
	sig := signWebhook([]byte("whsec_test"), raw, now)
	if !verifySignature(sig, raw, []byte("whsec_test"), now) {
		t.Fatal("alert webhook payload signature did not verify")
	}
}

func TestAlertEndpointsAuthAndValidation(t *testing.T) {
	pubURL, _ := url.Parse("https://mail.example.com")
	s := &Server{
		cfg:    &config.Config{MailHostname: "mail.example.com", PublicURL: pubURL},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		alerts: newAlertState(),
	}
	nonAdmin := &principal{User: User{ID: "u1", Email: "user@example.com", IsPlatformAdmin: false}}
	admin := &principal{User: User{ID: "a1", Email: "admin@example.com", IsPlatformAdmin: true}}

	handlers := []func(http.ResponseWriter, *http.Request, *principal){
		s.listAlertRules, s.updateAlertRule, s.listAlerts, s.getAlert, s.acknowledgeAlert, s.resolveAlert, s.testAlert,
	}
	for i, h := range handlers {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/alerts", nil)
		h(rr, req, nonAdmin)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("handler %d returned %d for non-admin, want 403", i, rr.Code)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/alerts?status=invalid", nil)
	s.listAlerts(rr, req, admin)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status returned %d, want 422", rr.Code)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/alerts?limit=500", nil)
	s.listAlerts(rr, req, admin)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid limit returned %d, want 422", rr.Code)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/alerts/not-a-uuid", nil)
	req.SetPathValue("alertId", "not-a-uuid")
	s.getAlert(rr, req, admin)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("invalid alertId returned %d, want 404", rr.Code)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/admin/alerts/rules/BAD-KEY!", strings.NewReader(`{}`))
	req.SetPathValue("ruleKey", "BAD-KEY!")
	s.updateAlertRule(rr, req, admin)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("invalid ruleKey returned %d, want 404", rr.Code)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/alerts/test", strings.NewReader(`{"unknown":true}`))
	s.testAlert(rr, req, admin)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown field in testAlert returned %d, want 400", rr.Code)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/alerts/rules", nil)
	req.Pattern = "GET /api/v1/admin/alerts/rules"
	if _, ok := s.apiKeyPrincipal(rr, req, apiKeyPrefix+"dummy"); ok || rr.Code != http.StatusForbidden {
		t.Fatalf("API key was allowed on admin alerts route: code=%d ok=%v", rr.Code, ok)
	}
}

func TestScrapeMetrics(t *testing.T) {
	var gotPassword string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		gotPassword = r.Header.Get("Password")
		w.Write([]byte("rspamd_actions_total{type=\"soft reject\"} 9\n"))
	}))
	defer ts.Close()

	s := &Server{cfg: &config.Config{}, alerts: newAlertState()}
	families, err := s.scrapeMetrics(context.Background(), ts.URL+"/metrics", map[string]string{"Password": "secret"})
	if err != nil {
		t.Fatalf("scrapeMetrics failed: %v", err)
	}
	if gotPassword != "secret" {
		t.Fatalf("Password header = %q, want secret", gotPassword)
	}
	if v, ok := extractRspamdRateLimits(families); !ok || v != 9 {
		t.Fatalf("extracted %v (ok=%v), want 9", v, ok)
	}
	if _, err := s.scrapeMetrics(context.Background(), ts.URL+"/missing", nil); err == nil {
		t.Fatal("expected error for 404 metrics endpoint")
	}
}

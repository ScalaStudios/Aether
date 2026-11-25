package api

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/redis/go-redis/v9"

	mailpkg "github.com/scalastudios/aether/internal/mail"
)

const (
	alertAdvisoryLockID  = 7316249
	alertEventWindow     = 15 * time.Minute
	alertDNSCheckCadence = 10 * time.Minute
	alertDMARCMinSamples = 10
)

type alertState struct {
	mu               sync.Mutex
	httpClient       *http.Client
	nowFn            func() time.Time
	sendMail         func(from mailpkg.Address, rcpts []string, raw []byte) error
	lastDNSCheck     time.Time
	postfixFailures  counterWindow
	rspamdRateLimits counterWindow
}

func newAlertState() *alertState {
	return &alertState{
		httpClient: &http.Client{Timeout: 8 * time.Second},
	}
}

func (s *Server) now() time.Time {
	if s.alerts != nil && s.alerts.nowFn != nil {
		return s.alerts.nowFn()
	}
	return time.Now().UTC()
}

type counterSample struct {
	at    time.Time
	delta float64
}

type counterWindow struct {
	initialized bool
	lastTotal   float64
	samples     []counterSample
}

func (c *counterWindow) Observe(total float64, now time.Time, window time.Duration) float64 {
	if !c.initialized {
		c.initialized = true
		c.lastTotal = total
		return 0
	}
	var delta float64
	if total >= c.lastTotal {
		delta = total - c.lastTotal
	} else {
		delta = total
	}
	c.lastTotal = total
	if delta > 0 {
		c.samples = append(c.samples, counterSample{at: now, delta: delta})
	}
	cutoff := now.Add(-window)
	kept := c.samples[:0]
	sum := 0.0
	for _, sample := range c.samples {
		if !sample.at.Before(cutoff) {
			kept = append(kept, sample)
			sum += sample.delta
		}
	}
	c.samples = kept
	return sum
}

type alertTransition struct {
	action string
	notify bool
	event  string
}

func decideAlertTransition(activeStatus string, lastNotifiedAt *time.Time, breached bool, cooldown time.Duration, now time.Time) alertTransition {
	if breached {
		switch activeStatus {
		case "":
			inCooldown := lastNotifiedAt != nil && now.Sub(*lastNotifiedAt) < cooldown
			return alertTransition{action: "fire", notify: !inCooldown, event: "alert.firing"}
		case "firing":
			shouldNotify := lastNotifiedAt == nil || now.Sub(*lastNotifiedAt) >= cooldown
			return alertTransition{action: "update", notify: shouldNotify, event: "alert.firing"}
		case "acknowledged":
			return alertTransition{action: "update", notify: false}
		}
	}
	if activeStatus == "firing" || activeStatus == "acknowledged" {
		return alertTransition{action: "resolve", notify: true, event: "alert.resolved"}
	}
	return alertTransition{action: "noop"}
}

type alertSample struct {
	fingerprint string
	orgID       *string
	targetType  string
	targetID    string
	targetLabel string
	value       float64
	title       string
	message     string
	details     map[string]any
}

type postfixQueueSnapshot struct {
	total         int
	oldestSeconds float64
	hasAge        bool
	byQueue       map[string]int
}

func labelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

func extractPostfixExporterSnapshot(families map[string]*dto.MetricFamily) (*postfixQueueSnapshot, float64, bool) {
	var snap *postfixQueueSnapshot
	if f, ok := families["postfix_showq_queue_depth"]; ok {
		snap = &postfixQueueSnapshot{byQueue: map[string]int{}}
		for _, m := range f.GetMetric() {
			count := int(math.Round(m.GetGauge().GetValue()))
			snap.total += count
			if q := labelValue(m, "queue"); q != "" {
				snap.byQueue[q] += count
			}
		}
		if ages, ok := families["postfix_showq_message_age_seconds"]; ok {
			snap.hasAge = true
			for _, m := range ages.GetMetric() {
				lower, bound, prev := 0.0, 0.0, 0.0
				for _, b := range m.GetHistogram().GetBucket() {
					n := float64(b.GetCumulativeCount()) + b.GetCumulativeCountFloat()
					if n > prev {
						lower = bound
					}
					prev = n
					if !math.IsInf(b.GetUpperBound(), 1) {
						bound = b.GetUpperBound()
					}
				}
				snap.oldestSeconds = max(snap.oldestSeconds, lower)
			}
		}
	}
	var failTotal float64
	hasFail := false
	if f, ok := families["postfix_smtp_messages_processed_total"]; ok {
		for _, m := range f.GetMetric() {
			if status := labelValue(m, "status"); status == "bounced" || status == "deferred" {
				failTotal += m.GetCounter().GetValue()
				hasFail = true
			}
		}
	}
	return snap, failTotal, hasFail
}

func extractRspamdRateLimits(families map[string]*dto.MetricFamily) (float64, bool) {
	f, ok := families["rspamd_actions_total"]
	if !ok {
		return 0, false
	}
	var total float64
	found := false
	for _, m := range f.GetMetric() {
		if labelValue(m, "type") == "soft reject" {
			total += m.GetCounter().GetValue() + m.GetUntyped().GetValue()
			found = true
		}
	}
	return total, found
}

type certExpiryInfo struct {
	daysRemaining    float64
	secondsRemaining float64
	notAfter         time.Time
	subject          string
	issuer           string
}

func readCertExpiry(path, hostname string, now time.Time) (*certExpiryInfo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for len(raw) > 0 {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		if cert.VerifyHostname(hostname) != nil {
			continue
		}
		remaining := cert.NotAfter.Sub(now)
		return &certExpiryInfo{
			daysRemaining:    remaining.Hours() / 24,
			secondsRemaining: remaining.Seconds(),
			notAfter:         cert.NotAfter.UTC(),
			subject:          cert.Subject.CommonName,
			issuer:           cert.Issuer.CommonName,
		}, nil
	}
	return nil, errors.New("no certificate for " + hostname + " found in file")
}

type diskUsageInfo struct {
	percent    float64
	totalBytes uint64
	usedBytes  uint64
	availBytes uint64
}

func diskUsageStatfs(path string) (*diskUsageInfo, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil, err
	}
	if st.Bsize <= 0 || st.Blocks == 0 {
		return nil, errors.New("filesystem reported zero capacity")
	}
	bsize := uint64(st.Bsize)
	total := st.Blocks * bsize
	used := (st.Blocks - st.Bfree) * bsize
	avail := st.Bavail * bsize
	usable := used + avail
	pct := 0.0
	if usable > 0 {
		pct = float64(used) * 100 / float64(usable)
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return &diskUsageInfo{percent: pct, totalBytes: total, usedBytes: used, availBytes: avail}, nil
}

func (s *Server) recordAlertEvent(ctx context.Context, bucket, label string) {
	if s.kv == nil {
		return
	}
	now := s.now()
	key := "aether:alerts:" + bucket
	member := strconv.FormatInt(now.UnixNano(), 10) + ":" + label
	pipe := s.kv.Pipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixNano()), Member: member})
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.Add(-alertEventWindow).UnixNano(), 10))
	pipe.Expire(ctx, key, alertEventWindow+5*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		s.log.Warn("recording alert event failed", "bucket", bucket, "err", err)
	}
}

func (s *Server) countAlertEvents(ctx context.Context, bucket string) (int64, []string, error) {
	if s.kv == nil {
		return 0, nil, nil
	}
	now := s.now()
	key := "aether:alerts:" + bucket
	cutoff := strconv.FormatInt(now.Add(-alertEventWindow).UnixNano(), 10)
	pipe := s.kv.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", cutoff)
	countCmd := pipe.ZCard(ctx, key)
	sampleCmd := pipe.ZRevRange(ctx, key, 0, 9)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, nil, err
	}
	rawMembers := sampleCmd.Val()
	labels := make([]string, 0, len(rawMembers))
	seen := map[string]struct{}{}
	for _, m := range rawMembers {
		_, label, ok := strings.Cut(m, ":")
		if !ok || label == "" {
			continue
		}
		if _, dup := seen[label]; dup {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	return countCmd.Val(), labels, nil
}

func (s *Server) scrapeMetrics(ctx context.Context, rawURL string, headers map[string]string) (map[string]*dto.MetricFamily, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.alerts.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	return parser.TextToMetricFamilies(io.LimitReader(resp.Body, 4<<20))
}

func readClamAVSignatureDate(ctx context.Context, addr string) (time.Time, string, error) {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return time.Time{}, "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("zVERSION\x00")); err != nil {
		return time.Time{}, "", err
	}
	reply, err := bufio.NewReader(io.LimitReader(conn, 4096)).ReadString(0)
	if err != nil {
		return time.Time{}, "", err
	}
	reply = strings.TrimRight(reply, "\x00\n")
	parts := strings.SplitN(reply, "/", 3)
	if len(parts) != 3 {
		return time.Time{}, "", fmt.Errorf("unexpected clamd VERSION reply %q", reply)
	}
	built, err := time.Parse("Mon Jan _2 15:04:05 2006", strings.TrimSpace(parts[2]))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("unexpected clamd VERSION reply %q", reply)
	}
	return built.UTC(), parts[0] + "/" + parts[1], nil
}

func (s *Server) RunAlertWorker(ctx context.Context) {
	interval := s.cfg.AlertEvalInterval
	if interval < 10*time.Second {
		interval = 60 * time.Second
	}
	initial := min(15*time.Second, interval)
	timer := time.NewTimer(initial)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.evaluateAlertsWithLock(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) evaluateAlertsWithLock(ctx context.Context) {
	evalCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	conn, err := s.db.Acquire(evalCtx)
	if err != nil {
		s.log.Error("acquiring connection for alert evaluation failed", "err", err)
		return
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(evalCtx, `SELECT pg_try_advisory_lock($1)`, alertAdvisoryLockID).Scan(&locked); err != nil {
		s.log.Error("acquiring alert evaluation lock failed", "err", err)
		return
	}
	if !locked {
		return
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer unlockCancel()
		conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, alertAdvisoryLockID)
	}()
	s.evaluateAlerts(evalCtx)
}

func (s *Server) evaluateAlerts(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT `+alertRuleColumns+` FROM alert_rules ORDER BY key`)
	if err != nil {
		s.log.Error("loading alert rules failed", "err", err)
		return
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (alertRule, error) { return scanAlertRule(row) })
	if err != nil {
		s.log.Error("loading alert rules failed", "err", err)
		return
	}
	rules := map[string]alertRule{}
	for _, r := range all {
		if r.Enabled {
			rules[r.Key] = r
		} else {
			s.resolveOrphanedRuleAlerts(ctx, r, nil)
		}
	}
	apply := func(key string, eval func(alertRule) (alertSample, bool)) {
		if r, ok := rules[key]; ok {
			if sample, ok := eval(r); ok {
				s.applyAlertSample(ctx, r, sample)
			}
		}
	}

	var postfixSnap *postfixQueueSnapshot
	var postfixFailTotal float64
	var hasPostfixFail bool
	if s.cfg.PostfixExporterURL != "" {
		if families, err := s.scrapeMetrics(ctx, s.cfg.PostfixExporterURL, nil); err == nil {
			postfixSnap, postfixFailTotal, hasPostfixFail = extractPostfixExporterSnapshot(families)
		} else {
			s.log.Warn("scraping postfix exporter failed", "err", err)
		}
	}
	if postfixSnap != nil {
		apply("mail_queue_size", func(r alertRule) (alertSample, bool) {
			return alertSample{
				fingerprint: "mail_queue_size",
				targetType:  "queue",
				targetID:    "postfix",
				targetLabel: "Postfix mail queue",
				value:       float64(postfixSnap.total),
				title:       "Mail queue size exceeded threshold",
				message:     fmt.Sprintf("Postfix mail queue currently holds %d messages (threshold: %s messages).", postfixSnap.total, formatFloat(r.Threshold)),
				details:     map[string]any{"queues": postfixSnap.byQueue},
			}, true
		})
		apply("mail_queue_age", func(r alertRule) (alertSample, bool) {
			return alertSample{
				fingerprint: "mail_queue_age",
				targetType:  "queue",
				targetID:    "postfix",
				targetLabel: "Postfix mail queue",
				value:       postfixSnap.oldestSeconds,
				title:       "Mail queue age exceeded threshold",
				message:     fmt.Sprintf("The oldest message in the Postfix mail queue has waited at least %s seconds (threshold: %s seconds).", formatFloat(postfixSnap.oldestSeconds), formatFloat(r.Threshold)),
				details:     map[string]any{"totalMessages": postfixSnap.total, "queues": postfixSnap.byQueue},
			}, postfixSnap.hasAge
		})
	}
	apply("delivery_failures", func(r alertRule) (alertSample, bool) {
		return s.evalDeliveryFailures(ctx, r, postfixFailTotal, hasPostfixFail)
	})
	apply("rate_limited_senders", func(r alertRule) (alertSample, bool) { return s.evalRateLimitedSenders(ctx, r) })
	apply("tls_cert_expiry", s.evalTLSCertExpiry)
	apply("clamav_signature_age", func(r alertRule) (alertSample, bool) { return s.evalClamAVSignatureAge(ctx, r) })
	if r, ok := rules["dmarc_failure_rate"]; ok {
		s.evalDMARCFailureRate(ctx, r)
	}
	apply("dovecot_acl_backlog", func(r alertRule) (alertSample, bool) { return s.evalDovecotACLBacklog(ctx, r) })
	apply("dovecot_kick_backlog", func(r alertRule) (alertSample, bool) { return s.evalDovecotKickBacklog(ctx, r) })
	apply("disk_vmail_usage", func(r alertRule) (alertSample, bool) { return s.evalDiskUsage(ctx, r, "vmail", s.cfg.VmailPath, 0) })
	apply("disk_pgdata_usage", func(r alertRule) (alertSample, bool) {
		return s.evalDiskUsage(ctx, r, "pgdata", s.cfg.PgdataPath, s.cfg.PgdataMaxBytes)
	})
	if r, ok := rules["dns_check_failure"]; ok {
		s.evalDNSChecks(ctx, r)
	}
	s.refreshAlertMetrics(ctx)
}

func (s *Server) evalDeliveryFailures(ctx context.Context, rule alertRule, postfixFailTotal float64, hasPostfixFail bool) (alertSample, bool) {
	now := s.now()
	var total float64
	details := map[string]any{"windowMinutes": int(alertEventWindow.Minutes())}

	apiCount, senders, err := s.countAlertEvents(ctx, "delivery_failures")
	if err != nil {
		s.log.Warn("counting delivery failures failed", "err", err)
		return alertSample{}, false
	}
	total += float64(apiCount)
	details["apiSubmissionFailures"] = apiCount
	if len(senders) > 0 {
		details["sampleSenders"] = senders
	}
	if hasPostfixFail {
		s.alerts.mu.Lock()
		postfixDelta := s.alerts.postfixFailures.Observe(postfixFailTotal, now, alertEventWindow)
		s.alerts.mu.Unlock()
		total += postfixDelta
		details["postfixFailures"] = math.Round(postfixDelta)
	}
	return alertSample{
		fingerprint: "delivery_failures",
		targetType:  "mail",
		targetID:    s.cfg.MailHostname,
		targetLabel: s.cfg.MailHostname,
		value:       total,
		title:       "Outbound mail delivery failures exceeded threshold",
		message:     fmt.Sprintf("%s delivery failures occurred in the last 15 minutes (threshold: %s).", formatFloat(total), formatFloat(rule.Threshold)),
		details:     details,
	}, true
}

func (s *Server) evalRateLimitedSenders(ctx context.Context, rule alertRule) (alertSample, bool) {
	now := s.now()
	var total float64
	details := map[string]any{"windowMinutes": int(alertEventWindow.Minutes())}

	apiCount, senders, err := s.countAlertEvents(ctx, "rate_limited_senders")
	if err != nil {
		s.log.Warn("counting rate-limited senders failed", "err", err)
		return alertSample{}, false
	}
	total += float64(apiCount)
	details["apiRateLimitEvents"] = apiCount
	if len(senders) > 0 {
		details["senders"] = senders
	}
	if s.cfg.RspamdMetricsURL != "" {
		headers := map[string]string{}
		if s.cfg.RspamdMetricsPassword != "" {
			headers["Password"] = s.cfg.RspamdMetricsPassword
		}
		if families, err := s.scrapeMetrics(ctx, s.cfg.RspamdMetricsURL, headers); err != nil {
			s.log.Warn("scraping rspamd metrics failed", "err", err)
		} else if rspamdTotal, ok := extractRspamdRateLimits(families); ok {
			s.alerts.mu.Lock()
			delta := s.alerts.rspamdRateLimits.Observe(rspamdTotal, now, alertEventWindow)
			s.alerts.mu.Unlock()
			total += delta
			details["rspamdSoftRejects"] = math.Round(delta)
		}
	}
	return alertSample{
		fingerprint: "rate_limited_senders",
		targetType:  "mail",
		targetID:    s.cfg.MailHostname,
		targetLabel: s.cfg.MailHostname,
		value:       total,
		title:       "Outbound sender rate-limit triggered",
		message:     fmt.Sprintf("%s sender rate-limit events recorded in the last 15 minutes (threshold: %s).", formatFloat(total), formatFloat(rule.Threshold)),
		details:     details,
	}, true
}

func (s *Server) evalTLSCertExpiry(rule alertRule) (alertSample, bool) {
	if s.cfg.MailTLSCAFile == "" {
		return alertSample{}, false
	}
	info, err := readCertExpiry(s.cfg.MailTLSCAFile, s.cfg.MailHostname, s.now())
	if err != nil {
		s.log.Warn("reading mail TLS certificate failed", "path", s.cfg.MailTLSCAFile, "err", err)
		return alertSample{}, false
	}
	tlsCertExpiry.Set(info.secondsRemaining)
	msg := fmt.Sprintf("Mail TLS certificate for %s expires in %s days on %s (threshold: %s days).",
		s.cfg.MailHostname, formatFloat(info.daysRemaining), info.notAfter.Format(time.RFC3339), formatFloat(rule.Threshold))
	if info.daysRemaining <= 0 {
		msg = fmt.Sprintf("Mail TLS certificate for %s expired on %s.", s.cfg.MailHostname, info.notAfter.Format(time.RFC3339))
	}
	return alertSample{
		fingerprint: "tls_cert_expiry",
		targetType:  "certificate",
		targetID:    s.cfg.MailHostname,
		targetLabel: s.cfg.MailHostname,
		value:       info.daysRemaining,
		title:       "Mail TLS certificate expiring soon",
		message:     msg,
		details: map[string]any{
			"notAfter":      info.notAfter.Format(time.RFC3339),
			"daysRemaining": math.Round(info.daysRemaining*100) / 100,
			"subject":       info.subject,
			"issuer":        info.issuer,
		},
	}, true
}

func (s *Server) evalClamAVSignatureAge(ctx context.Context, rule alertRule) (alertSample, bool) {
	if s.cfg.ClamAVAddr == "" {
		return alertSample{}, false
	}
	built, version, err := readClamAVSignatureDate(ctx, s.cfg.ClamAVAddr)
	if err != nil {
		s.log.Warn("reading ClamAV signature version failed", "addr", s.cfg.ClamAVAddr, "err", err)
		return alertSample{}, false
	}
	days := s.now().Sub(built).Hours() / 24
	return alertSample{
		fingerprint: "clamav_signature_age",
		targetType:  "antivirus",
		targetID:    "clamav",
		targetLabel: "ClamAV signatures",
		value:       days,
		title:       "ClamAV signatures are out of date",
		message:     fmt.Sprintf("The ClamAV signature database (%s) was built %s days ago (threshold: %s days).", version, formatFloat(days), formatFloat(rule.Threshold)),
		details:     map[string]any{"version": version, "builtAt": built.Format(time.RFC3339)},
	}, true
}

var requiredDNSPurposes = []string{"mx", "spf", "dkim", "dmarc"}

func (s *Server) evaluateDomainDNSRecords(ctx context.Context, d *domain, keys []dkimKey) ([]dnsRecord, bool) {
	required := slices.DeleteFunc(s.expectedRecords(d, keys), func(rec dnsRecord) bool { return !slices.Contains(requiredDNSPurposes, rec.Purpose) })
	s.checkRecords(ctx, required)
	var failed []dnsRecord
	for _, rec := range required {
		switch rec.Status {
		case "error":
			return nil, false
		case "missing", "mismatch":
			failed = append(failed, rec)
		}
	}
	return failed, ctx.Err() == nil
}

func (s *Server) evalDNSChecks(ctx context.Context, rule alertRule) {
	now := s.now()
	s.alerts.mu.Lock()
	due := s.alerts.lastDNSCheck.IsZero() || now.Sub(s.alerts.lastDNSCheck) >= alertDNSCheckCadence
	if due {
		s.alerts.lastDNSCheck = now
	}
	s.alerts.mu.Unlock()
	if !due {
		return
	}
	rows, err := s.db.Query(ctx, `SELECT `+domainColumns+` FROM domains WHERE active AND verified_at IS NOT NULL ORDER BY name`)
	if err != nil {
		s.log.Error("loading domains for DNS alert check failed", "err", err)
		return
	}
	var domains []*domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			rows.Close()
			s.log.Error("scanning domain for DNS alert check failed", "err", err)
			return
		}
		domains = append(domains, d)
	}
	rows.Close()

	activeFingerprints := map[string]struct{}{}
	failingDomains := 0
	for _, d := range domains {
		fp := "dns_check_failure:" + d.ID
		activeFingerprints[fp] = struct{}{}
		keys, err := s.dkimKeys(ctx, d)
		if err != nil {
			s.log.Warn("loading dkim keys for the dns check failed", "domain", d.Name, "err", err)
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		failed, ok := s.evaluateDomainDNSRecords(checkCtx, d, keys)
		cancel()
		if !ok {
			continue
		}
		if len(failed) > 0 {
			failingDomains++
		}
		failedSummary := make([]map[string]string, 0, len(failed))
		names := make([]string, 0, len(failed))
		for _, rec := range failed {
			failedSummary = append(failedSummary, map[string]string{"purpose": rec.Purpose, "type": rec.Type, "name": rec.Name, "status": rec.Status})
			names = append(names, strings.ToUpper(rec.Purpose)+" ("+rec.Status+")")
		}
		orgID := d.OrgID
		s.applyAlertSample(ctx, rule, alertSample{
			fingerprint: fp,
			orgID:       &orgID,
			targetType:  "domain",
			targetID:    d.ID,
			targetLabel: d.Name,
			value:       float64(len(failed)),
			title:       "DNS check failed for " + d.Name,
			message:     fmt.Sprintf("%d required DNS record(s) failed for %s: %s.", len(failed), d.Name, strings.Join(names, ", ")),
			details:     map[string]any{"domain": d.Name, "failedRecords": failedSummary},
		})
	}
	domainsDNSFailing.Set(float64(failingDomains))
	s.resolveOrphanedRuleAlerts(ctx, rule, activeFingerprints)
}

func (s *Server) evalDMARCFailureRate(ctx context.Context, rule alertRule) {
	rows, err := s.db.Query(ctx, `
		SELECT d.id, d.org_id, d.name,
		       coalesce(sum(rec.count), 0)::bigint,
		       coalesce(sum(rec.count) FILTER (WHERE NOT rec.dkim_aligned AND NOT rec.spf_aligned), 0)::bigint
		FROM domains d
		LEFT JOIN dmarc_reports rep ON rep.domain_id = d.id AND rep.ends_at > now() - interval '24 hours'
		LEFT JOIN dmarc_records rec ON rec.report_id = rep.id
		WHERE d.active AND d.verified_at IS NOT NULL
		GROUP BY d.id, d.org_id, d.name
		ORDER BY d.name`)
	if err != nil {
		s.log.Error("evaluating DMARC failure rate failed", "err", err)
		return
	}
	type domainDMARCStats struct {
		id, orgID, name string
		total, failed   int64
	}
	stats, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domainDMARCStats, error) {
		var st domainDMARCStats
		return st, row.Scan(&st.id, &st.orgID, &st.name, &st.total, &st.failed)
	})
	if err != nil {
		s.log.Error("reading DMARC stats failed", "err", err)
		return
	}
	activeFingerprints := map[string]struct{}{}
	failingDomains := 0
	for _, st := range stats {
		fp := "dmarc_failure_rate:" + st.id
		activeFingerprints[fp] = struct{}{}
		rate := 0.0
		if st.total >= alertDMARCMinSamples {
			rate = float64(st.failed) * 100 / float64(st.total)
		}
		if rule.breached(rate) {
			failingDomains++
		}
		orgID := st.orgID
		s.applyAlertSample(ctx, rule, alertSample{
			fingerprint: fp,
			orgID:       &orgID,
			targetType:  "domain",
			targetID:    st.id,
			targetLabel: st.name,
			value:       rate,
			title:       "DMARC failure rate elevated for " + st.name,
			message:     fmt.Sprintf("%s%% of %d reported messages for %s failed DMARC alignment in the last 24 hours (threshold: %s%%).", formatFloat(rate), st.total, st.name, formatFloat(rule.Threshold)),
			details: map[string]any{
				"domain":         st.name,
				"totalMessages":  st.total,
				"failedMessages": st.failed,
				"windowHours":    24,
			},
		})
	}
	dmarcFailDomains.Set(float64(failingDomains))
	s.resolveOrphanedRuleAlerts(ctx, rule, activeFingerprints)
}

func (s *Server) evalDovecotACLBacklog(ctx context.Context, rule alertRule) (alertSample, bool) {
	var age float64
	var pending int64
	err := s.db.QueryRow(ctx, `SELECT coalesce(extract(epoch FROM now() - min(acl_pending_since)), 0)::float8, count(*) FROM mailboxes WHERE acl_pending AND active`).Scan(&age, &pending)
	if err != nil {
		s.log.Error("checking Dovecot ACL backlog failed", "err", err)
		return alertSample{}, false
	}
	return alertSample{
		fingerprint: "dovecot_acl_backlog",
		targetType:  "dovecot",
		targetID:    "acl",
		targetLabel: "Dovecot ACL reconciliation",
		value:       age,
		title:       "Dovecot ACL reconciliation backlog exceeded threshold",
		message:     fmt.Sprintf("Oldest pending shared-mailbox ACL reconciliation has waited %s seconds across %d mailbox(es) (threshold: %s seconds).", formatFloat(age), pending, formatFloat(rule.Threshold)),
		details:     map[string]any{"pendingMailboxes": pending},
	}, true
}

func (s *Server) evalDovecotKickBacklog(ctx context.Context, rule alertRule) (alertSample, bool) {
	var age float64
	var pending int64
	err := s.db.QueryRow(ctx, `SELECT coalesce(extract(epoch FROM now() - min(created_at)), 0)::float8, count(*) FROM mail_kicks`).Scan(&age, &pending)
	if err != nil {
		s.log.Error("checking Dovecot kick backlog failed", "err", err)
		return alertSample{}, false
	}
	return alertSample{
		fingerprint: "dovecot_kick_backlog",
		targetType:  "dovecot",
		targetID:    "kick",
		targetLabel: "Dovecot session kicks",
		value:       age,
		title:       "Dovecot session kick backlog exceeded threshold",
		message:     fmt.Sprintf("Oldest pending IMAP session kick has waited %s seconds across %d pending kick(s) (threshold: %s seconds).", formatFloat(age), pending, formatFloat(rule.Threshold)),
		details:     map[string]any{"pendingKicks": pending},
	}, true
}

func (s *Server) evalDiskUsage(ctx context.Context, rule alertRule, volume, path string, maxBytes int64) (alertSample, bool) {
	var pct float64
	details := map[string]any{"volume": volume}
	measured := false

	if path != "" {
		if info, err := diskUsageStatfs(path); err == nil {
			pct = info.percent
			details["path"] = path
			details["totalBytes"] = info.totalBytes
			details["usedBytes"] = info.usedBytes
			details["availBytes"] = info.availBytes
			details["source"] = "statfs"
			measured = true
		} else {
			s.log.Warn("reading disk usage failed", "volume", volume, "path", path, "err", err)
		}
	}
	if !measured && volume == "pgdata" && maxBytes > 0 {
		var dbBytes int64
		if err := s.db.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&dbBytes); err == nil {
			pct = float64(dbBytes) * 100 / float64(maxBytes)
			if pct > 100 {
				pct = 100
			}
			details["usedBytes"] = dbBytes
			details["maxBytes"] = maxBytes
			details["source"] = "pg_database_size"
			measured = true
		}
	}
	if !measured {
		return alertSample{}, false
	}
	diskUsagePercent.WithLabelValues(volume).Set(pct)
	return alertSample{
		fingerprint: rule.Key,
		targetType:  "volume",
		targetID:    volume,
		targetLabel: volume,
		value:       pct,
		title:       fmt.Sprintf("Disk usage high on %s", volume),
		message:     fmt.Sprintf("Storage volume %s is at %s%% utilization (threshold: %s%%).", volume, formatFloat(pct), formatFloat(rule.Threshold)),
		details:     details,
	}, true
}

func (s *Server) resolveOrphanedRuleAlerts(ctx context.Context, rule alertRule, validFingerprints map[string]struct{}) {
	rows, err := s.db.Query(ctx, `SELECT id, fingerprint FROM alerts WHERE rule_key = $1 AND status IN ('firing', 'acknowledged')`, rule.Key)
	if err != nil {
		return
	}
	type activeRow struct{ id, fp string }
	actives, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (activeRow, error) {
		var a activeRow
		return a, row.Scan(&a.id, &a.fp)
	})
	if err != nil {
		return
	}
	for _, a := range actives {
		if _, ok := validFingerprints[a.fp]; ok {
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE alerts SET status = 'resolved', resolved_at = now(), last_seen_at = now() WHERE id = $1 AND status <> 'resolved'`, a.id); err != nil {
			continue
		}
		if item, err := scanAlertItem(s.db.QueryRow(ctx, alertSelectQuery+` WHERE a.id = $1`, a.id)); err == nil {
			s.notifyAlert(ctx, rule, item, "alert.resolved")
		}
	}
}

func (s *Server) applyAlertSample(ctx context.Context, rule alertRule, sample alertSample) {
	now := s.now()
	s.db.Exec(ctx, `UPDATE alert_rules SET last_evaluated_at = $2 WHERE key = $1`, rule.Key, now)
	breached := rule.breached(sample.value)
	var activeID, activeStatus string
	var lastNotified *time.Time
	err := s.db.QueryRow(ctx, `SELECT id, status, last_notified_at FROM alerts WHERE fingerprint = $1 AND status IN ('firing', 'acknowledged')`, sample.fingerprint).
		Scan(&activeID, &activeStatus, &lastNotified)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.log.Error("loading active alert failed", "fingerprint", sample.fingerprint, "err", err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		activeStatus = ""
		if breached {
			s.db.QueryRow(ctx, `SELECT last_notified_at FROM alerts WHERE fingerprint = $1 AND last_notified_at IS NOT NULL ORDER BY last_notified_at DESC LIMIT 1`, sample.fingerprint).Scan(&lastNotified)
		}
	}

	tr := decideAlertTransition(activeStatus, lastNotified, breached, rule.cooldown(), now)
	detailsJSON, _ := json.Marshal(sample.details)
	if len(detailsJSON) == 0 {
		detailsJSON = []byte("{}")
	}

	switch tr.action {
	case "fire":
		var id string
		err := s.db.QueryRow(ctx, `
			INSERT INTO alerts (rule_key, fingerprint, org_id, target_type, target_id, target_label, severity, status, title, message, current_value, threshold_value, unit, details, fired_at, last_seen_at, last_notified_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'firing', $8, $9, $10, $11, $12, $13, $14, $14, $15)
			RETURNING id`,
			rule.Key, sample.fingerprint, sample.orgID, sample.targetType, sample.targetID, sample.targetLabel,
			rule.Severity, sample.title, sample.message, sample.value, rule.Threshold, rule.Unit, detailsJSON, now, lastNotified).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return
		}
		if err != nil {
			s.log.Error("creating firing alert failed", "rule", rule.Key, "fingerprint", sample.fingerprint, "err", err)
			return
		}
		if tr.notify {
			if item, err := scanAlertItem(s.db.QueryRow(ctx, alertSelectQuery+` WHERE a.id = $1`, id)); err == nil {
				if s.notifyAlert(ctx, rule, item, tr.event) {
					s.db.Exec(ctx, `UPDATE alerts SET last_notified_at = $2 WHERE id = $1`, id, now)
				}
			}
		}

	case "update":
		_, err := s.db.Exec(ctx, `
			UPDATE alerts
			SET severity = $2, title = $3, message = $4, current_value = $5, threshold_value = $6,
			    details = $7, occurrences = occurrences + 1, last_seen_at = $8
			WHERE id = $1`,
			activeID, rule.Severity, sample.title, sample.message, sample.value, rule.Threshold, detailsJSON, now)
		if err != nil {
			s.log.Error("updating active alert failed", "id", activeID, "err", err)
			return
		}
		if tr.notify {
			if item, err := scanAlertItem(s.db.QueryRow(ctx, alertSelectQuery+` WHERE a.id = $1`, activeID)); err == nil {
				if s.notifyAlert(ctx, rule, item, tr.event) {
					s.db.Exec(ctx, `UPDATE alerts SET last_notified_at = $2 WHERE id = $1`, activeID, now)
				}
			}
		}

	case "resolve":
		_, err := s.db.Exec(ctx, `
			UPDATE alerts
			SET status = 'resolved', resolved_at = $2, last_seen_at = $2, current_value = $3, details = $4
			WHERE id = $1 AND status <> 'resolved'`,
			activeID, now, sample.value, detailsJSON)
		if err != nil {
			s.log.Error("auto-resolving alert failed", "id", activeID, "err", err)
			return
		}
		if tr.notify {
			if item, err := scanAlertItem(s.db.QueryRow(ctx, alertSelectQuery+` WHERE a.id = $1`, activeID)); err == nil {
				s.notifyAlert(ctx, rule, item, tr.event)
			}
		}
	}
}

func (s *Server) refreshAlertMetrics(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT rule_key, severity, count(*) FROM alerts WHERE status IN ('firing', 'acknowledged') GROUP BY rule_key, severity`)
	if err != nil {
		return
	}
	defer rows.Close()
	alertsFiring.Reset()
	for rows.Next() {
		var ruleKey, severity string
		var count float64
		if err := rows.Scan(&ruleKey, &severity, &count); err == nil {
			alertsFiring.WithLabelValues(ruleKey, severity).Set(count)
		}
	}
}

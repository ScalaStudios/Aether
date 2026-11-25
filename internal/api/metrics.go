package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	reportsSent         = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_reports_sent_total", Help: "Aggregate reports accepted by Postfix or an HTTPS collector."}, []string{"kind"})
	metricsRegistry     = prometheus.NewRegistry()
	httpRequests        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_http_requests_total", Help: "HTTP requests by route pattern, method, and status code."}, []string{"route", "method", "code"})
	httpDuration        = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "aether_http_request_duration_seconds", Help: "HTTP request latency by route pattern.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"route"})
	authAttempts        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_auth_attempts_total", Help: "Sign-in attempts by method and result."}, []string{"method", "result"})
	mailSends           = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_mail_send_total", Help: "Messages submitted from Aether Mail by result."}, []string{"result"})
	mailBackendErrs     = prometheus.NewCounter(prometheus.CounterOpts{Name: "aether_mail_backend_errors_total", Help: "Failed requests to the IMAP, submission, or ManageSieve services."})
	webhookEvents       = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_identity_webhook_events_total", Help: "Identity provider webhook events by result."}, []string{"result"})
	kickBacklog         = prometheus.NewGauge(prometheus.GaugeOpts{Name: "aether_mail_kick_backlog_seconds", Help: "Age in seconds of the oldest IMAP session kick still waiting for a successful doveadm call; 0 when none is pending."})
	spamLearned         = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_spam_learned_total", Help: "Messages sent to Rspamd for Bayes learning after a move into or out of Junk, by class and result."}, []string{"class", "result"})
	authBlocks          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_auth_address_blocks_total", Help: "Addresses blocked after repeated failed sign-ins, by the protocol of the last failure."}, []string{"protocol"})
	aclBacklog          = prometheus.NewGauge(prometheus.GaugeOpts{Name: "aether_mail_acl_backlog_seconds", Help: "Age in seconds of the oldest active mailbox whose Dovecot rights are still waiting for a successful reconciliation; 0 when none is pending."})
	queueMessages       = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "aether_mail_queue_messages", Help: "Messages in the Postfix queue by queue: maildrop, incoming, active, deferred and hold. Read every minute."}, []string{"queue"})
	queueOldest         = prometheus.NewGauge(prometheus.GaugeOpts{Name: "aether_mail_queue_oldest_seconds", Help: "Age in seconds of the oldest message in the Postfix queue that is not on hold; 0 when there is none."})
	idleWatches         = prometheus.NewGauge(prometheus.GaugeOpts{Name: "aether_mail_idle_watches", Help: "IMAP IDLE connections this process holds for live mail."})
	notifySent          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_notifications_total", Help: "Notification deliveries by channel and result."}, []string{"channel", "result"})
	subscriptionRefresh = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_calendar_subscription_refresh_total", Help: "Calendar subscription refreshes by result."}, []string{"result"})
)

var (
	mailRateLimited   = prometheus.NewCounter(prometheus.CounterOpts{Name: "aether_mail_rate_limited_total", Help: "Outbound mail send attempts rejected by rate limiting."})
	tlsCertExpiry     = prometheus.NewGauge(prometheus.GaugeOpts{Name: "aether_tls_cert_expiry_seconds", Help: "Seconds remaining until the mail TLS certificate expires."})
	domainsDNSFailing = prometheus.NewGauge(prometheus.GaugeOpts{Name: "aether_domains_dns_failing", Help: "Number of active verified domains failing required DNS checks."})
	dmarcFailDomains  = prometheus.NewGauge(prometheus.GaugeOpts{Name: "aether_dmarc_failing_domains", Help: "Number of active verified domains exceeding the DMARC failure rate threshold."})
	diskUsagePercent  = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "aether_disk_usage_percent", Help: "Measured disk usage percentage by storage volume."}, []string{"volume"})
	alertsFiring      = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "aether_alerts_firing", Help: "Number of active alerts by rule and severity."}, []string{"rule", "severity"})
)

func init() {
	metricsRegistry.MustRegister(httpRequests, httpDuration, authAttempts, mailSends, mailBackendErrs, webhookEvents, kickBacklog, aclBacklog, spamLearned, queueMessages, queueOldest, authBlocks, reportsSent, idleWatches, notifySent, subscriptionRefresh,
		mailRateLimited, tlsCertExpiry, domainsDNSFailing, dmarcFailDomains, diskUsagePercent, alertsFiring,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.code)).Inc()
		httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

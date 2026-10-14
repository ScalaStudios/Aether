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
	metricsRegistry = prometheus.NewRegistry()
	httpRequests    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_http_requests_total", Help: "HTTP requests by route pattern, method, and status code."}, []string{"route", "method", "code"})
	httpDuration    = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "aether_http_request_duration_seconds", Help: "HTTP request latency by route pattern.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"route"})
	authAttempts    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_auth_attempts_total", Help: "Sign-in attempts by method and result."}, []string{"method", "result"})
	mailSends       = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_mail_send_total", Help: "Messages submitted from Aether Mail by result."}, []string{"result"})
	mailBackendErrs = prometheus.NewCounter(prometheus.CounterOpts{Name: "aether_mail_backend_errors_total", Help: "Failed requests to the IMAP, submission, or ManageSieve services."})
	webhookEvents   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aether_identity_webhook_events_total", Help: "Identity provider webhook events by result."}, []string{"result"})
)

func init() {
	metricsRegistry.MustRegister(httpRequests, httpDuration, authAttempts, mailSends, mailBackendErrs, webhookEvents,
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

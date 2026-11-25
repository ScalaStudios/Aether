package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"

	"github.com/scalastudios/aether/internal/api"
	"github.com/scalastudios/aether/internal/auth"
	"github.com/scalastudios/aether/internal/config"
	"github.com/scalastudios/aether/internal/db"
	"github.com/scalastudios/aether/internal/mail"
	"github.com/scalastudios/aether/internal/mailtrace"
	"github.com/scalastudios/aether/internal/secrets"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(log)
	case "migrate":
		err = migrate(log)
	case "bootstrap-admin":
		err = bootstrapAdmin(os.Args[2:])
	case "healthcheck":
		err = healthcheck()
	case "mail-trace":
		err = mailTrace(log)
	case "mail-events":
		err = mailEvents(log)
	default:
		err = fmt.Errorf("unknown command %q; use serve, migrate, bootstrap-admin, healthcheck, mail-trace, or mail-events", cmd)
	}
	if err != nil {
		log.Error("aether exited with an error", "command", cmd, "err", err)
		os.Exit(1)
	}
}

func migrate(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(context.Background(), pool)
	log.Info("migrations complete", "applied", applied)
	return err
}

func mailTrace(log *slog.Logger) error {
	url := os.Getenv("AETHER_DATABASE_URL")
	if url == "" {
		return errors.New("AETHER_DATABASE_URL is required")
	}
	retention, err := strconv.Atoi(cmp.Or(os.Getenv("AETHER_MAIL_TRACE_RETENTION_DAYS"), "30"))
	if err != nil || retention < 1 {
		return errors.New("AETHER_MAIL_TRACE_RETENTION_DAYS must be a whole number of days, at least 1")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, url)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	mailtrace.New(pool, log, cmp.Or(os.Getenv("AETHER_MAIL_LOG"), "/var/log/postfix/mail.log"), retention).Run(ctx)
	return nil
}

func mailEvents(log *slog.Logger) error {
	dsn := os.Getenv("AETHER_DATABASE_URL")
	if dsn == "" {
		return errors.New("AETHER_DATABASE_URL is required")
	}
	path := os.Getenv("AETHER_MAIL_LOG")
	if path == "" {
		path = "/var/log/postfix/mail.log"
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	log.Info("following the postfix log for delivery events", "path", path)
	return api.New(&config.Config{}, pool, nil, nil, nil, log).FollowMailLog(ctx, path)
}

func serve(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTracing, err := setupTracing(ctx)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer shutdownTracing()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		log.Info("applied migrations", "versions", applied)
	}
	box, err := secrets.NewBox(cfg.SecretKey)
	if err != nil {
		return err
	}
	kv := redis.NewClient(&redis.Options{Addr: cfg.ValkeyAddr, Password: cfg.ValkeyPassword})
	defer kv.Close()
	if err := redisotel.InstrumentTracing(kv, redisotel.WithDBStatement(false)); err != nil {
		return err
	}
	mc, err := mail.NewClient(cfg.IMAPAddr, cfg.SubmissionAddr, cfg.SieveAddr, cfg.SieveRelayAddr, cfg.SieveRelayPassword, cfg.MailHostname, cfg.MailTLSCAFile, cfg.DovecotMasterUser, cfg.DovecotMasterPassword, cfg.DovecotAPIURL, cfg.DovecotAPIKey)
	if err != nil {
		return err
	}
	srv := api.New(cfg, pool, box, kv, mc, log)
	if err := srv.SyncDKIM(ctx); err != nil {
		log.Error("DKIM key sync incomplete; affected domains send unsigned mail", "err", err)
	}
	if err := srv.SyncAttachmentPolicies(ctx); err != nil {
		log.Error("attachment policy sync incomplete; affected domains do not filter attachments", "err", err)
	}
	go srv.RunScheduler(ctx)
	go srv.RunReports(ctx)
	go srv.RunAccessWorkers(ctx)
	go srv.RunQueueMetrics(ctx)
	go srv.RunRetention(ctx)
	go srv.RunResourceWorker(ctx)
	go srv.RunQuotaWatcher(ctx)
	go srv.RunNotifications(ctx)
	go srv.RunFileLinkCleanup(ctx)
	go srv.RunSubscriptionRefresh(ctx)
	httpServer := &http.Server{
		Addr: cfg.Listen, Handler: tracedHandler(srv.Handler()),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Minute, WriteTimeout: 30 * time.Minute, IdleTimeout: 120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	errCh := make(chan error, 7)
	if cfg.MetricsListen != "" {
		metrics := &http.Server{Addr: cfg.MetricsListen, Handler: api.MetricsHandler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			log.Info("metrics listening", "addr", cfg.MetricsListen)
			errCh <- metrics.ListenAndServe()
		}()
		defer metrics.Close()
	}
	reports := srv.ReportServer(cfg.ReportListen)
	go func() {
		log.Info("report receiver listening", "addr", cfg.ReportListen)
		errCh <- reports.ListenAndServe()
	}()
	defer reports.Close()
	policy, err := net.Listen("tcp", cfg.PolicyListen)
	if err != nil {
		return err
	}
	go func() {
		log.Info("postfix policy service listening", "addr", cfg.PolicyListen)
		errCh <- srv.ServePolicy(policy)
	}()
	defer policy.Close()
	for _, m := range []struct {
		addr  string
		after bool
	}{{cfg.MilterListen, false}, {cfg.MilterAfterListen, true}} {
		ln, err := net.Listen("tcp", m.addr)
		if err != nil {
			return err
		}
		go func() {
			log.Info("mail flow milter listening", "addr", m.addr, "after_spam_filter", m.after)
			errCh <- srv.ServeMilter(ln, m.after)
		}()
		defer ln.Close()
	}
	auditMilter, err := net.Listen("tcp", cfg.AuditMilterListen)
	if err != nil {
		return err
	}
	go func() {
		log.Info("mailbox audit milter listening", "addr", cfg.AuditMilterListen)
		errCh <- srv.ServeAuditMilter(auditMilter)
	}()
	defer auditMilter.Close()
	if cfg.MailDBPassword != "" {
		outboundSASL, err := net.Listen("tcp", cfg.OutboundSASLListen)
		if err != nil {
			return err
		}
		defer outboundSASL.Close()
		go func() { errCh <- srv.ServeOutboundSASL(outboundSASL) }()
	}
	go func() {
		log.Info("aether api listening", "addr", cfg.Listen, "public_url", cfg.PublicURL.String())
		errCh <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func setupTracing(ctx context.Context) (func(), error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func() {}, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("aether-api")), resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		provider.Shutdown(ctx)
	}, nil
}

func tracedHandler(h http.Handler) http.Handler {
	traced := otelhttp.NewHandler(h, "http",
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" && r.URL.Path != "/readyz" }),
		otelhttp.WithSpanNameFormatter(func(op string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern
			}
			return r.Method
		}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			h.ServeHTTP(w, r)
			return
		}
		traced.ServeHTTP(w, r)
	})
}

func bootstrapAdmin(args []string) error {
	fs := flag.NewFlagSet("bootstrap-admin", flag.ContinueOnError)
	email := fs.String("email", "", "administrator email address")
	name := fs.String("name", "Administrator", "display name")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from standard input instead of generating one")
	resetTwoFactor := fs.Bool("reset-two-factor", false, "remove the administrator's authenticator app, security keys and recovery codes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	address := strings.ToLower(strings.TrimSpace(*email))
	if !strings.Contains(address, "@") {
		return errors.New("--email is required")
	}
	password := secrets.Token(15)
	if *passwordStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		password = strings.TrimRight(line, "\r\n")
		if len(password) < 12 {
			return errors.New("the password must be at least 12 characters")
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `
		INSERT INTO users (email, display_name, password_hash, is_platform_admin) VALUES ($1, $2, $3, true)
		ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash, is_platform_admin = true, status = 'active', updated_at = now()`,
		address, *name, auth.HashPassword(password))
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE email = $1)`, address); err != nil {
		return err
	}
	if *resetTwoFactor {
		_, err := pool.Exec(ctx, `
			WITH u AS (SELECT id FROM users WHERE email = $1), totp AS (DELETE FROM user_totp WHERE user_id IN (SELECT id FROM u)), codes AS (DELETE FROM user_recovery_codes WHERE user_id IN (SELECT id FROM u))
			DELETE FROM user_security_keys WHERE user_id IN (SELECT id FROM u)`, address)
		if err != nil {
			return err
		}
	}
	pool.Exec(ctx, `INSERT INTO audit_events (action, target_type, target_id, metadata) VALUES ('platform_admin.bootstrap', 'user', $1, jsonb_build_object('source', 'cli', 'resetTwoFactor', $2::boolean))`, address, *resetTwoFactor)
	if tag.RowsAffected() == 1 {
		fmt.Printf("Platform administrator ready: %s\n", address)
	}
	if !*passwordStdin {
		fmt.Printf("Generated password (shown once): %s\n", password)
	}
	return nil
}

func healthcheck() error {
	port := "8080"
	if addr := os.Getenv("AETHER_LISTEN"); addr != "" {
		_, port, _ = net.SplitHostPort(addr)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz returned %d", resp.StatusCode)
	}
	return nil
}

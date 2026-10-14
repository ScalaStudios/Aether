package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/scalastudios/aether/internal/api"
	"github.com/scalastudios/aether/internal/auth"
	"github.com/scalastudios/aether/internal/config"
	"github.com/scalastudios/aether/internal/db"
	"github.com/scalastudios/aether/internal/mail"
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
	default:
		err = fmt.Errorf("unknown command %q; use serve, migrate, bootstrap-admin, or healthcheck", cmd)
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

func serve(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
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
	mc, err := mail.NewClient(cfg.IMAPAddr, cfg.SubmissionAddr, cfg.MailHostname, cfg.MailTLSCAFile, cfg.DovecotMasterUser, cfg.DovecotMasterPassword)
	if err != nil {
		return err
	}
	srv := api.New(cfg, pool, box, kv, mc, log)
	if err := srv.SyncDKIM(ctx); err != nil {
		log.Error("DKIM key sync incomplete; affected domains send unsigned mail", "err", err)
	}
	httpServer := &http.Server{
		Addr: cfg.Listen, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	errCh := make(chan error, 1)
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

func bootstrapAdmin(args []string) error {
	fs := flag.NewFlagSet("bootstrap-admin", flag.ContinueOnError)
	email := fs.String("email", "", "administrator email address")
	name := fs.String("name", "Administrator", "display name")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from standard input instead of generating one")
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
	pool.Exec(ctx, `INSERT INTO audit_events (action, target_type, target_id, metadata) VALUES ('platform_admin.bootstrap', 'user', $1, '{"source":"cli"}')`, address)
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

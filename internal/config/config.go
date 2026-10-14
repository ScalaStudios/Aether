package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Listen                string
	DatabaseURL           string
	PublicURL             *url.URL
	SecretKey             []byte
	MailHostname          string
	IMAPAddr              string
	SubmissionAddr        string
	SieveAddr             string
	MailTLSCAFile         string
	DovecotMasterUser     string
	DovecotMasterPassword string
	ValkeyAddr            string
	ValkeyPassword        string
	LocalLogin            bool
	TrustProxy            bool
	AllowHTTPIssuers      bool
}

func Load() (*Config, error) {
	c := &Config{
		Listen:                env("AETHER_LISTEN", ":8080"),
		DatabaseURL:           os.Getenv("AETHER_DATABASE_URL"),
		MailHostname:          strings.ToLower(os.Getenv("AETHER_MAIL_HOSTNAME")),
		IMAPAddr:              env("AETHER_IMAP_ADDR", "dovecot:143"),
		SubmissionAddr:        env("AETHER_SUBMISSION_ADDR", "postfix:587"),
		SieveAddr:             env("AETHER_SIEVE_ADDR", "dovecot:4190"),
		MailTLSCAFile:         os.Getenv("AETHER_MAIL_TLS_CA_FILE"),
		DovecotMasterUser:     env("AETHER_DOVECOT_MASTER_USER", "aether-webmail"),
		DovecotMasterPassword: os.Getenv("AETHER_DOVECOT_MASTER_PASSWORD"),
		ValkeyAddr:            env("AETHER_VALKEY_ADDR", "valkey:6379"),
		ValkeyPassword:        os.Getenv("AETHER_VALKEY_PASSWORD"),
		LocalLogin:            env("AETHER_LOCAL_LOGIN", "true") == "true",
		TrustProxy:            os.Getenv("AETHER_TRUST_PROXY") == "true",
		AllowHTTPIssuers:      os.Getenv("AETHER_OIDC_ALLOW_HTTP") == "true",
	}
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("AETHER_DATABASE_URL is required"))
	}
	if c.MailHostname == "" {
		errs = append(errs, errors.New("AETHER_MAIL_HOSTNAME is required"))
	}
	if c.DovecotMasterPassword == "" {
		errs = append(errs, errors.New("AETHER_DOVECOT_MASTER_PASSWORD is required"))
	}
	u, err := url.Parse(os.Getenv("AETHER_PUBLIC_URL"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, errors.New("AETHER_PUBLIC_URL must be an absolute http(s) URL"))
	}
	c.PublicURL = u
	key, err := base64.StdEncoding.DecodeString(os.Getenv("AETHER_SECRET_KEY"))
	if err != nil || len(key) != 32 {
		errs = append(errs, fmt.Errorf("AETHER_SECRET_KEY must be 32 bytes, base64 encoded (generate with: openssl rand -base64 32)"))
	}
	c.SecretKey = key
	return c, errors.Join(errs...)
}

func (c *Config) SecureCookies() bool {
	return c.PublicURL.Scheme == "https"
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

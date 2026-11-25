package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen                string
	MetricsListen         string
	DatabaseURL           string
	PublicURL             *url.URL
	SecretKey             []byte
	MailHostname          string
	IMAPAddr              string
	SubmissionAddr        string
	SieveAddr             string
	SieveRelayAddr        string
	SieveRelayPassword    string
	MailTLSCAFile         string
	DovecotMasterUser     string
	DovecotMasterPassword string
	DovecotAPIURL         string
	DovecotAPIKey         string
	RspamdControllerURL   string
	RspamdPassword        string
	PostfixAPIURL         string
	PostfixAPIKey         string
	ValkeyAddr            string
	ValkeyPassword        string
	LocalLogin            bool
	TrustProxy            bool
	AllowHTTPIssuers      bool
	MTASTSMode            string
	WebhookAllowPrivate   bool
	ReportListen          string
	PolicyListen          string
	MilterListen          string
	MilterAfterListen     string
	AuditMilterListen     string
	OutboundSASLListen    string
	MailDBPassword        string
	CustomCSS             bool
	AuthBlockThreshold    int
	AuthBlockAllowNets    []netip.Prefix
	QuotaCheckInterval    time.Duration
	SendReports           bool
	ReportFrom            string
	ReportOrgName         string
	MailLog               string
	ReportRelayHost       string
	VAPIDPublicKey        string
	VAPIDPrivateKey       string
	APNSCertFile          string
	APNSKeyFile           string
	APNSSandbox           bool
	MessageSizeLimit      int64
	MaxLinkBytes          int64
	MaxStorageBytes       int64
}

func Load() (*Config, error) {
	c := &Config{
		Listen:                env("AETHER_LISTEN", ":8080"),
		MetricsListen:         os.Getenv("AETHER_METRICS_LISTEN"),
		DatabaseURL:           os.Getenv("AETHER_DATABASE_URL"),
		MailHostname:          strings.ToLower(os.Getenv("AETHER_MAIL_HOSTNAME")),
		IMAPAddr:              env("AETHER_IMAP_ADDR", "dovecot:143"),
		SubmissionAddr:        env("AETHER_SUBMISSION_ADDR", "postfix:587"),
		SieveAddr:             env("AETHER_SIEVE_ADDR", "dovecot:4190"),
		SieveRelayAddr:        env("AETHER_SIEVE_RELAY_ADDR", "postfix:10025"),
		SieveRelayPassword:    os.Getenv("AETHER_SIEVE_RELAY_PASSWORD"),
		MailTLSCAFile:         os.Getenv("AETHER_MAIL_TLS_CA_FILE"),
		DovecotMasterUser:     env("AETHER_DOVECOT_MASTER_USER", "aether-webmail"),
		DovecotMasterPassword: os.Getenv("AETHER_DOVECOT_MASTER_PASSWORD"),
		DovecotAPIURL:         env("AETHER_DOVECOT_API_URL", "http://dovecot:8089"),
		DovecotAPIKey:         os.Getenv("AETHER_DOVECOT_API_KEY"),
		RspamdControllerURL:   env("AETHER_RSPAMD_CONTROLLER_URL", "http://rspamd:11334"),
		RspamdPassword:        os.Getenv("AETHER_RSPAMD_CONTROLLER_PASSWORD"),
		PostfixAPIURL:         env("AETHER_POSTFIX_API_URL", "http://postfix:8025"),
		PostfixAPIKey:         os.Getenv("AETHER_POSTFIX_API_KEY"),
		ValkeyAddr:            env("AETHER_VALKEY_ADDR", "valkey:6379"),
		ValkeyPassword:        os.Getenv("AETHER_VALKEY_PASSWORD"),
		LocalLogin:            env("AETHER_LOCAL_LOGIN", "true") == "true",
		TrustProxy:            os.Getenv("AETHER_TRUST_PROXY") == "true",
		AllowHTTPIssuers:      os.Getenv("AETHER_OIDC_ALLOW_HTTP") == "true",
		MTASTSMode:            env("AETHER_MTA_STS_MODE", "testing"),
		WebhookAllowPrivate:   os.Getenv("AETHER_WEBHOOK_ALLOW_PRIVATE") == "true",
		ReportListen:          env("AETHER_REPORT_LISTEN", ":2424"),
		PolicyListen:          env("AETHER_POLICY_LISTEN", ":10040"),
		MilterListen:          env("AETHER_MILTER_LISTEN", ":10045"),
		MilterAfterListen:     env("AETHER_MILTER_AFTER_LISTEN", ":10046"),
		AuditMilterListen:     env("AETHER_AUDIT_MILTER_LISTEN", ":10047"),
		OutboundSASLListen:    env("AETHER_OUTBOUND_SASL_LISTEN", ":10041"),
		MailDBPassword:        os.Getenv("AETHER_MAIL_DB_PASSWORD"),
		CustomCSS:             env("AETHER_CUSTOM_CSS", "true") == "true",
		SendReports:           env("AETHER_SEND_REPORTS", "true") == "true",
		ReportFrom:            strings.ToLower(os.Getenv("AETHER_REPORT_FROM")),
		ReportOrgName:         os.Getenv("AETHER_REPORT_ORG_NAME"),
		MailLog:               os.Getenv("AETHER_MAIL_LOG"),
		ReportRelayHost:       os.Getenv("AETHER_RELAYHOST"),
		VAPIDPublicKey:        os.Getenv("AETHER_VAPID_PUBLIC_KEY"),
		VAPIDPrivateKey:       os.Getenv("AETHER_VAPID_PRIVATE_KEY"),
		APNSCertFile:          os.Getenv("AETHER_APNS_CERT_FILE"),
		APNSKeyFile:           os.Getenv("AETHER_APNS_KEY_FILE"),
		APNSSandbox:           os.Getenv("AETHER_APNS_SANDBOX") == "true",
	}
	var errs []error
	var err error
	if c.MessageSizeLimit, err = envInt64("AETHER_MESSAGE_SIZE_LIMIT", 50<<20, 1, 2147483647); err != nil {
		errs = append(errs, err)
	}
	if c.MaxLinkBytes, err = envInt64("AETHER_MAX_LINK_BYTES", 2<<30, 1, 8<<30); err != nil {
		errs = append(errs, err)
	}
	if c.MaxStorageBytes, err = envInt64("AETHER_MAX_STORAGE_BYTES", 100<<30, 0, 100<<40); err != nil {
		errs = append(errs, err)
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("AETHER_DATABASE_URL is required"))
	}
	if c.MailHostname == "" {
		errs = append(errs, errors.New("AETHER_MAIL_HOSTNAME is required"))
	}
	if c.DovecotMasterPassword == "" {
		errs = append(errs, errors.New("AETHER_DOVECOT_MASTER_PASSWORD is required"))
	}
	if c.DovecotAPIKey == "" {
		errs = append(errs, errors.New("AETHER_DOVECOT_API_KEY is required"))
	}
	interval, err := time.ParseDuration(env("AETHER_QUOTA_CHECK_INTERVAL", "5m"))
	if err != nil || interval < 10*time.Second {
		errs = append(errs, errors.New("AETHER_QUOTA_CHECK_INTERVAL must be a duration of at least 10s, for example 5m"))
	}
	c.QuotaCheckInterval = interval
	if c.ReportFrom != "" {
		addr, err := mail.ParseAddress(c.ReportFrom)
		if err != nil || addr.Address != c.ReportFrom || strings.ContainsAny(c.ReportFrom, "\r\n") {
			errs = append(errs, errors.New("AETHER_REPORT_FROM must be an email address"))
		}
	}
	if c.MTASTSMode != "testing" && c.MTASTSMode != "enforce" {
		errs = append(errs, errors.New("AETHER_MTA_STS_MODE must be testing or enforce"))
	}
	if (c.VAPIDPublicKey == "") != (c.VAPIDPrivateKey == "") {
		errs = append(errs, errors.New("AETHER_VAPID_PUBLIC_KEY and AETHER_VAPID_PRIVATE_KEY must both be set, or both left empty"))
	}
	if (c.APNSCertFile == "") != (c.APNSKeyFile == "") {
		errs = append(errs, errors.New("AETHER_APNS_CERT_FILE and AETHER_APNS_KEY_FILE must both be set, or both left empty"))
	}
	u, err := url.Parse(os.Getenv("AETHER_PUBLIC_URL"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, errors.New("AETHER_PUBLIC_URL must be an absolute http(s) URL"))
	}
	c.PublicURL = u
	if c.AuthBlockThreshold, err = strconv.Atoi(env("AETHER_AUTH_BLOCK_THRESHOLD", "10")); err != nil || c.AuthBlockThreshold < 0 {
		errs = append(errs, errors.New("AETHER_AUTH_BLOCK_THRESHOLD must be a whole number, 0 to turn blocking off"))
	}
	for _, n := range strings.Split(env("AETHER_AUTH_BLOCK_ALLOW_NETS", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"), ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(n)
		if err != nil {
			addr, aerr := netip.ParseAddr(n)
			if aerr != nil {
				errs = append(errs, fmt.Errorf("AETHER_AUTH_BLOCK_ALLOW_NETS: %q is not an address or network", n))
				continue
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		c.AuthBlockAllowNets = append(c.AuthBlockAllowNets, prefix.Masked())
	}
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

func envInt64(key string, fallback, min, max int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, min, max)
	}
	return n, nil
}

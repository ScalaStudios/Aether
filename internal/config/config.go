package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAuditRetentionDays = 0
	MaxAuditRetentionDays     = 3650
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
	BackupMX              []string
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
	AuditRetentionDays    int
	AuditSyslogAddr       string
	AuditSyslogNetwork    string
	AuditSyslogFacility   int
	AuditSyslogAppName    string
	AuditOTLPEndpoint     string
	AuditOTLPHeaders      map[string]string
	AuditOTLPServiceName  string
	AlertEvalInterval     time.Duration
	AlertFrom             string
	PostfixExporterURL    string
	RspamdMetricsURL      string
	RspamdMetricsPassword string
	VmailPath             string
	PgdataPath            string
	PgdataMaxBytes        int64
	ClamAVAddr            string
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
		BackupMX:              strings.Fields(strings.ToLower(strings.ReplaceAll(os.Getenv("AETHER_BACKUP_MX"), ",", " "))),
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
		AuditRetentionDays:    DefaultAuditRetentionDays,
		AuditSyslogAddr:       strings.TrimSpace(os.Getenv("AETHER_AUDIT_SYSLOG_ADDR")),
		AuditSyslogNetwork:    strings.ToLower(strings.TrimSpace(env("AETHER_AUDIT_SYSLOG_NETWORK", "udp"))),
		AuditSyslogAppName:    strings.TrimSpace(env("AETHER_AUDIT_SYSLOG_APP_NAME", "aether")),
		AuditOTLPServiceName:  strings.TrimSpace(env("OTEL_SERVICE_NAME", "aether-api")),
		AlertEvalInterval:     60 * time.Second,
		AlertFrom:             strings.ToLower(strings.TrimSpace(os.Getenv("AETHER_ALERT_FROM"))),
		PostfixExporterURL:    strings.TrimSpace(os.Getenv("AETHER_POSTFIX_EXPORTER_URL")),
		RspamdMetricsURL:      strings.TrimSpace(os.Getenv("AETHER_RSPAMD_METRICS_URL")),
		RspamdMetricsPassword: os.Getenv("AETHER_RSPAMD_METRICS_PASSWORD"),
		VmailPath:             strings.TrimSpace(os.Getenv("AETHER_VMAIL_PATH")),
		PgdataPath:            strings.TrimSpace(os.Getenv("AETHER_PGDATA_PATH")),
		ClamAVAddr:            strings.TrimSpace(os.Getenv("AETHER_CLAMAV_ADDR")),
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
	if raw := strings.TrimSpace(os.Getenv("AETHER_ALERT_EVAL_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 10*time.Second {
			errs = append(errs, errors.New("AETHER_ALERT_EVAL_INTERVAL must be a duration of at least 10s"))
		} else {
			c.AlertEvalInterval = d
		}
	}
	if raw := strings.TrimSpace(os.Getenv("AETHER_PGDATA_MAX_BYTES")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			errs = append(errs, errors.New("AETHER_PGDATA_MAX_BYTES must be a positive integer"))
		} else {
			c.PgdataMaxBytes = n
		}
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
	if raw := strings.TrimSpace(os.Getenv("AETHER_AUDIT_RETENTION_DAYS")); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days < 0 || days > MaxAuditRetentionDays {
			errs = append(errs, fmt.Errorf("AETHER_AUDIT_RETENTION_DAYS must be an integer between 0 and %d", MaxAuditRetentionDays))
		} else {
			c.AuditRetentionDays = days
		}
	}
	if c.AuditSyslogNetwork != "udp" && c.AuditSyslogNetwork != "tcp" {
		errs = append(errs, errors.New("AETHER_AUDIT_SYSLOG_NETWORK must be udp or tcp"))
	}
	if c.AuditSyslogAddr != "" {
		host, portStr, err := net.SplitHostPort(c.AuditSyslogAddr)
		port, portErr := strconv.Atoi(portStr)
		if err != nil || host == "" || portErr != nil || port < 1 || port > 65535 {
			errs = append(errs, errors.New("AETHER_AUDIT_SYSLOG_ADDR must be host:port"))
		}
	}
	facility, err := parseSyslogFacility(env("AETHER_AUDIT_SYSLOG_FACILITY", "auth"))
	if err != nil {
		errs = append(errs, err)
	}
	c.AuditSyslogFacility = facility
	rawOTLP := strings.TrimSpace(os.Getenv("AETHER_AUDIT_OTLP_ENDPOINT"))
	if rawOTLP == "" {
		rawOTLP = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"))
	}
	if rawOTLP != "" {
		ou, err := normalizeOTLPLogsEndpoint(rawOTLP)
		if err != nil {
			errs = append(errs, err)
		} else {
			c.AuditOTLPEndpoint = ou
		}
	}
	rawHeaders := strings.TrimSpace(os.Getenv("AETHER_AUDIT_OTLP_HEADERS"))
	if rawHeaders == "" {
		rawHeaders = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS"))
	}
	if rawHeaders == "" {
		rawHeaders = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	}
	if rawHeaders != "" {
		c.AuditOTLPHeaders = parseOTLPHeaders(rawHeaders)
	}
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

var syslogFacilities = map[string]int{
	"kern": 0, "user": 1, "mail": 2, "daemon": 3, "auth": 4, "security": 4,
	"syslog": 5, "lpr": 6, "news": 7, "uucp": 8, "cron": 9, "authpriv": 10, "ftp": 11,
	"local0": 16, "local1": 17, "local2": 18, "local3": 19,
	"local4": 20, "local5": 21, "local6": 22, "local7": 23,
}

func parseSyslogFacility(raw string) (int, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if f, ok := syslogFacilities[v]; ok {
		return f, nil
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 23 {
		return n, nil
	}
	return 0, errors.New("AETHER_AUDIT_SYSLOG_FACILITY must be a syslog facility name or integer 0..23")
}

func normalizeOTLPLogsEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("AETHER_AUDIT_OTLP_ENDPOINT must be an absolute http(s) URL")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/logs"
	}
	return u.String(), nil
}

func parseOTLPHeaders(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if decoded, err := url.QueryUnescape(v); err == nil {
			v = decoded
		}
		out[k] = v
	}
	return out
}

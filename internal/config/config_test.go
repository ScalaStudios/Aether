package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AETHER_DATABASE_URL", "postgres://aether:pw@localhost/aether")
	t.Setenv("AETHER_MAIL_HOSTNAME", "mail.example.com")
	t.Setenv("AETHER_DOVECOT_MASTER_PASSWORD", "master-pw")
	t.Setenv("AETHER_DOVECOT_API_KEY", "api-key")
	t.Setenv("AETHER_PUBLIC_URL", "https://mail.example.com")
	t.Setenv("AETHER_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
}

func TestLoadAuditDefaults(t *testing.T) {
	setRequiredEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if c.AuditRetentionDays != 0 {
		t.Errorf("AuditRetentionDays = %d, want 0", c.AuditRetentionDays)
	}
	if c.AuditSyslogNetwork != "udp" || c.AuditSyslogFacility != 4 || c.AuditSyslogAppName != "aether" {
		t.Errorf("unexpected syslog defaults: %+v", c)
	}
	if c.AuditOTLPEndpoint != "" || c.AuditOTLPServiceName != "aether-api" {
		t.Errorf("unexpected OTLP defaults: endpoint=%q service=%q", c.AuditOTLPEndpoint, c.AuditOTLPServiceName)
	}
}

func TestLoadAuditCustomSettings(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AETHER_AUDIT_RETENTION_DAYS", "90")
	t.Setenv("AETHER_AUDIT_SYSLOG_ADDR", "siem.internal:514")
	t.Setenv("AETHER_AUDIT_SYSLOG_NETWORK", "TCP")
	t.Setenv("AETHER_AUDIT_SYSLOG_FACILITY", "local0")
	t.Setenv("AETHER_AUDIT_SYSLOG_APP_NAME", "aether-audit")
	t.Setenv("AETHER_AUDIT_OTLP_ENDPOINT", "http://otel-collector:4318")
	t.Setenv("AETHER_AUDIT_OTLP_HEADERS", "Authorization=Bearer%20tok123,X-Tenant=acme")
	t.Setenv("OTEL_SERVICE_NAME", "aether-prod")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if c.AuditRetentionDays != 90 {
		t.Errorf("AuditRetentionDays = %d, want 90", c.AuditRetentionDays)
	}
	if c.AuditSyslogAddr != "siem.internal:514" || c.AuditSyslogNetwork != "tcp" || c.AuditSyslogFacility != 16 || c.AuditSyslogAppName != "aether-audit" {
		t.Errorf("unexpected syslog config: addr=%q net=%q fac=%d app=%q", c.AuditSyslogAddr, c.AuditSyslogNetwork, c.AuditSyslogFacility, c.AuditSyslogAppName)
	}
	if c.AuditOTLPEndpoint != "http://otel-collector:4318/v1/logs" {
		t.Errorf("AuditOTLPEndpoint = %q", c.AuditOTLPEndpoint)
	}
	if c.AuditOTLPHeaders["Authorization"] != "Bearer tok123" || c.AuditOTLPHeaders["X-Tenant"] != "acme" {
		t.Errorf("AuditOTLPHeaders = %+v", c.AuditOTLPHeaders)
	}
	if c.AuditOTLPServiceName != "aether-prod" {
		t.Errorf("AuditOTLPServiceName = %q", c.AuditOTLPServiceName)
	}
}

func TestLoadAuditValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"negative retention", map[string]string{"AETHER_AUDIT_RETENTION_DAYS": "-1"}, "AETHER_AUDIT_RETENTION_DAYS"},
		{"excessive retention", map[string]string{"AETHER_AUDIT_RETENTION_DAYS": "4000"}, "AETHER_AUDIT_RETENTION_DAYS"},
		{"bad syslog network", map[string]string{"AETHER_AUDIT_SYSLOG_NETWORK": "sctp"}, "AETHER_AUDIT_SYSLOG_NETWORK"},
		{"bad syslog addr", map[string]string{"AETHER_AUDIT_SYSLOG_ADDR": "not-host-port"}, "AETHER_AUDIT_SYSLOG_ADDR"},
		{"bad syslog facility", map[string]string{"AETHER_AUDIT_SYSLOG_FACILITY": "bogus"}, "AETHER_AUDIT_SYSLOG_FACILITY"},
		{"bad otlp url", map[string]string{"AETHER_AUDIT_OTLP_ENDPOINT": "ftp://collector:4318"}, "AETHER_AUDIT_OTLP_ENDPOINT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() err = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadAuditOTLPFallbackEnv(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "https://otel.example.com/custom/logs")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-api-key=secret")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if c.AuditOTLPEndpoint != "https://otel.example.com/custom/logs" {
		t.Errorf("AuditOTLPEndpoint = %q", c.AuditOTLPEndpoint)
	}
	if c.AuditOTLPHeaders["x-api-key"] != "secret" {
		t.Errorf("AuditOTLPHeaders = %+v", c.AuditOTLPHeaders)
	}
}

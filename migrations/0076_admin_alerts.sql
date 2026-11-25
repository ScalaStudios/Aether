CREATE TABLE alert_rules (
    key text PRIMARY KEY,
    name text NOT NULL,
    description text NOT NULL,
    category text NOT NULL CHECK (category IN ('mail', 'security', 'dns', 'dovecot', 'storage')),
    scope text NOT NULL CHECK (scope IN ('platform', 'domain')),
    severity text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'critical')),
    enabled boolean NOT NULL DEFAULT true,
    threshold double precision NOT NULL CHECK (threshold >= 0),
    unit text NOT NULL,
    comparator text NOT NULL DEFAULT 'gte' CHECK (comparator IN ('gte', 'lte')),
    cooldown_seconds int NOT NULL DEFAULT 900 CHECK (cooldown_seconds BETWEEN 60 AND 604800),
    notify_email boolean NOT NULL DEFAULT true,
    notify_webhook boolean NOT NULL DEFAULT true,
    email_recipients text[] NOT NULL DEFAULT '{}',
    last_evaluated_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE alerts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_key text NOT NULL REFERENCES alert_rules (key) ON DELETE CASCADE,
    fingerprint text NOT NULL,
    org_id uuid REFERENCES organizations (id) ON DELETE CASCADE,
    target_type text NOT NULL DEFAULT '',
    target_id text NOT NULL DEFAULT '',
    target_label text NOT NULL DEFAULT '',
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    status text NOT NULL DEFAULT 'firing' CHECK (status IN ('firing', 'acknowledged', 'resolved')),
    title text NOT NULL,
    message text NOT NULL,
    current_value double precision,
    threshold_value double precision,
    unit text NOT NULL DEFAULT '',
    details jsonb NOT NULL DEFAULT '{}',
    occurrences int NOT NULL DEFAULT 1,
    fired_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    last_notified_at timestamptz,
    acknowledged_at timestamptz,
    acknowledged_by uuid REFERENCES users (id) ON DELETE SET NULL,
    resolved_at timestamptz,
    resolved_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX alerts_active_fingerprint_idx ON alerts (fingerprint) WHERE status IN ('firing', 'acknowledged');
CREATE INDEX alerts_status_fired_idx ON alerts (status, fired_at DESC);
CREATE INDEX alerts_rule_fired_idx ON alerts (rule_key, fired_at DESC);
CREATE INDEX alerts_org_fired_idx ON alerts (org_id, fired_at DESC) WHERE org_id IS NOT NULL;
CREATE INDEX alerts_fingerprint_notified_idx ON alerts (fingerprint, last_notified_at DESC) WHERE last_notified_at IS NOT NULL;

INSERT INTO alert_rules (key, name, description, category, scope, severity, enabled, threshold, unit, comparator, cooldown_seconds, notify_email, notify_webhook) VALUES
    ('mail_queue_size', 'Mail queue size', 'Fires when the Postfix mail queue reaches the configured message count.', 'mail', 'platform', 'warning', true, 50, 'messages', 'gte', 900, true, true),
    ('mail_queue_age', 'Mail queue age', 'Fires when the oldest message waiting in the Postfix mail queue exceeds the configured age.', 'mail', 'platform', 'warning', true, 1800, 'seconds', 'gte', 900, true, true),
    ('delivery_failures', 'Delivery failures', 'Fires when outbound bounces, deferrals, or submissions the mail server rejected in the last 15 minutes reach the configured count.', 'mail', 'platform', 'critical', true, 10, 'failures', 'gte', 900, true, true),
    ('rate_limited_senders', 'Rate-limited senders', 'Fires when outbound sender rate-limit events in the last 15 minutes reach the configured count.', 'mail', 'platform', 'warning', true, 1, 'events', 'gte', 900, true, true),
    ('tls_cert_expiry', 'TLS certificate expiry', 'Fires when the mail server TLS certificate expires within the configured number of days.', 'security', 'platform', 'critical', true, 14, 'days', 'lte', 86400, true, true),
    ('clamav_signature_age', 'ClamAV signature age', 'Fires when the ClamAV virus signature database is older than the configured number of days.', 'security', 'platform', 'warning', true, 2, 'days', 'gte', 86400, true, true),
    ('dns_check_failure', 'Domain DNS check failures', 'Fires per verified domain when required DNS records (MX, SPF, DKIM, DMARC) fail live checks.', 'dns', 'domain', 'warning', true, 1, 'records', 'gte', 3600, true, true),
    ('dmarc_failure_rate', 'DMARC failure rate', 'Fires per verified domain when recent DMARC aggregate reports show unaligned failures at or above the configured percentage.', 'dns', 'domain', 'warning', true, 10, 'percent', 'gte', 3600, true, true),
    ('dovecot_acl_backlog', 'Dovecot ACL backlog', 'Fires when the oldest pending shared-mailbox rights reconciliation exceeds the configured age.', 'dovecot', 'platform', 'warning', true, 300, 'seconds', 'gte', 900, true, true),
    ('dovecot_kick_backlog', 'Dovecot kick backlog', 'Fires when the oldest pending IMAP session kick exceeds the configured age.', 'dovecot', 'platform', 'critical', true, 120, 'seconds', 'gte', 900, true, true),
    ('disk_vmail_usage', 'Mail storage (vmail) disk use', 'Fires when the vmail volume reaches the configured disk utilization percentage.', 'storage', 'platform', 'critical', true, 85, 'percent', 'gte', 3600, true, true),
    ('disk_pgdata_usage', 'Database (pgdata) disk use', 'Fires when PostgreSQL storage reaches the configured disk utilization percentage.', 'storage', 'platform', 'critical', true, 85, 'percent', 'gte', 3600, true, true)
ON CONFLICT (key) DO NOTHING;

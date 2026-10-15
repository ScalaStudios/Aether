CREATE TABLE dmarc_reports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_id uuid NOT NULL REFERENCES domains (id) ON DELETE CASCADE,
    reporter text NOT NULL,
    report_id text NOT NULL,
    begins_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    policy text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (domain_id, reporter, report_id)
);

CREATE INDEX dmarc_reports_domain_idx ON dmarc_reports (domain_id, ends_at DESC);

CREATE TABLE dmarc_records (
    report_id uuid NOT NULL REFERENCES dmarc_reports (id) ON DELETE CASCADE,
    source_ip inet NOT NULL,
    count int NOT NULL,
    disposition text NOT NULL,
    dkim_aligned boolean NOT NULL,
    spf_aligned boolean NOT NULL,
    header_from text NOT NULL
);

CREATE INDEX dmarc_records_report_idx ON dmarc_records (report_id);

CREATE OR REPLACE VIEW mail_aliases AS
SELECT a.address, unnest(a.destinations) AS destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT mu.address, mu.address FROM mail_users mu
UNION ALL
SELECT 'dmarc-reports@' || d.name, 'reports@aether-reports.internal'
FROM domains d WHERE d.active AND d.verified_at IS NOT NULL;

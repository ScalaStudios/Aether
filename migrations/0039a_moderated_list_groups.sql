CREATE OR REPLACE VIEW mail_aliases AS
SELECT a.address, unnest(CASE WHEN a.moderated THEN ARRAY['list-' || a.id || '@aether-reports.internal'] ELSE a.destinations END) AS destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT mu.address, mu.address FROM mail_users mu
UNION ALL
SELECT 'dmarc-reports@' || d.name, 'reports@aether-reports.internal'
FROM domains d WHERE d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT DISTINCT a.address, lm.address
FROM aliases a
JOIN domains d ON d.id = a.domain_id
JOIN list_dynamic_members lm ON lm.alias_id = a.id
WHERE a.active AND NOT a.moderated AND d.active AND d.verified_at IS NOT NULL AND NOT lm.address = ANY (a.destinations);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_aliases TO aether_mail;
    END IF;
END $$;

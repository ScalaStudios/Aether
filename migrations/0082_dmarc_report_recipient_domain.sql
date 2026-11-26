CREATE OR REPLACE VIEW mail_alias_routes AS
SELECT a.address, unnest(CASE WHEN a.moderated THEN ARRAY['list-' || a.id || '@aether-reports.internal'] ELSE a.destinations END) AS destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT mu.address, mu.address FROM mail_users mu
UNION ALL
SELECT 'dmarc-reports@' || d.name, 'reports+' || d.id || '@aether-reports.internal'
FROM domains d WHERE d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT DISTINCT a.address, lm.address
FROM aliases a
JOIN domains d ON d.id = a.domain_id
JOIN list_dynamic_members lm ON lm.alias_id = a.id
WHERE a.active AND NOT a.moderated AND d.active AND d.verified_at IS NOT NULL AND NOT lm.address = ANY (a.destinations)
UNION ALL
SELECT t.local_part || '@' || mad.alias_name, t.address
FROM mail_alias_domains mad
JOIN (
    SELECT a.domain_id, a.local_part, a.address FROM aliases a WHERE a.active AND a.local_part <> ''
    UNION
    SELECT m.domain_id, m.local_part, m.address FROM mailboxes m JOIN mail_users mu ON mu.id = m.id
) t ON t.domain_id = mad.target_id
WHERE t.local_part <> 'dmarc-reports';

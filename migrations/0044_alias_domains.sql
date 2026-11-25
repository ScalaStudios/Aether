ALTER TABLE domains
    ADD CONSTRAINT domains_id_org_key UNIQUE (id, org_id),
    ADD COLUMN alias_of uuid,
    ADD COLUMN alias_send_as boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT domains_alias_of_fkey FOREIGN KEY (alias_of, org_id) REFERENCES domains (id, org_id),
    ADD CONSTRAINT domains_alias_of_check CHECK (alias_of <> id);

CREATE INDEX domains_alias_of_idx ON domains (alias_of) WHERE alias_of IS NOT NULL;

CREATE VIEW mail_alias_domains AS
SELECT ad.name AS alias_name, td.id AS target_id, td.name AS target_name, ad.alias_send_as AS send_as
FROM domains ad
JOIN domains td ON td.id = ad.alias_of
WHERE ad.active AND ad.verified_at IS NOT NULL AND td.active AND td.verified_at IS NOT NULL;

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

ALTER VIEW mail_senders RENAME TO mail_primary_senders;

CREATE VIEW mail_senders AS
SELECT sender, login FROM mail_primary_senders
UNION
SELECT split_part(ps.sender, '@', 1) || '@' || mad.alias_name, ps.login
FROM mail_primary_senders ps
JOIN mail_alias_domains mad ON mad.target_name = split_part(ps.sender, '@', 2)
WHERE mad.send_as;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_senders, mail_alias_domains TO aether_mail;
    END IF;
END $$;

CREATE OR REPLACE VIEW mail_restricted_lists AS
SELECT a.address
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND (a.accept_from <> 'anyone' OR a.moderated) AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT a.local_part || '@' || mad.alias_name
FROM aliases a
JOIN mail_alias_domains mad ON mad.target_id = a.domain_id
WHERE a.active AND (a.accept_from <> 'anyone' OR a.moderated) AND a.local_part <> ''
UNION ALL
SELECT m.address
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
WHERE m.kind = 'resource' AND m.active AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT m.local_part || '@' || mad.alias_name
FROM mailboxes m
JOIN mail_alias_domains mad ON mad.target_id = m.domain_id
WHERE m.kind = 'resource' AND m.active;

ALTER TABLE aliases
    ADD COLUMN kind text NOT NULL DEFAULT 'alias' CHECK (kind IN ('alias', 'list')),
    ADD COLUMN name text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    ADD COLUMN accept_from text NOT NULL DEFAULT 'anyone' CHECK (accept_from IN ('anyone', 'organization', 'members', 'allowlist')),
    ADD COLUMN allowed_senders text[] NOT NULL DEFAULT '{}' CHECK (cardinality(allowed_senders) <= 1000),
    ADD COLUMN send_as text[] NOT NULL DEFAULT '{}' CHECK (cardinality(send_as) <= 1000),
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE aliases DROP CONSTRAINT aliases_destinations_check;
ALTER TABLE aliases ADD CONSTRAINT aliases_destinations_check
    CHECK (cardinality(destinations) >= 1 AND cardinality(destinations) <= CASE WHEN kind = 'list' THEN 5000 ELSE 100 END);

CREATE TABLE alias_owners (
    alias_id uuid NOT NULL REFERENCES aliases (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (alias_id, user_id)
);

CREATE INDEX alias_owners_user_idx ON alias_owners (user_id);

CREATE OR REPLACE VIEW mail_senders AS
SELECT mu.address AS sender, mu.address AS login FROM mail_users mu
UNION
SELECT a.address, dest.destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
CROSS JOIN LATERAL unnest(CASE WHEN a.kind = 'list' THEN a.send_as ELSE a.destinations END) AS dest(destination)
JOIN mail_users mu ON mu.address = dest.destination
WHERE a.active AND d.active AND d.verified_at IS NOT NULL AND a.local_part <> '';

CREATE VIEW mail_restricted_lists AS
SELECT a.address
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND a.accept_from <> 'anyone' AND d.active AND d.verified_at IS NOT NULL;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_senders, mail_restricted_lists TO aether_mail;
    END IF;
END $$;

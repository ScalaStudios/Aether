ALTER TABLE aliases
    ADD COLUMN moderated boolean NOT NULL DEFAULT false,
    ADD COLUMN moderation_exempt text[] NOT NULL DEFAULT '{}' CHECK (cardinality(moderation_exempt) <= 1000),
    ADD CONSTRAINT aliases_moderated_list CHECK (kind = 'list' OR NOT moderated);

CREATE TABLE alias_moderators (
    alias_id uuid NOT NULL REFERENCES aliases (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (alias_id, user_id)
);

CREATE INDEX alias_moderators_user_idx ON alias_moderators (user_id);

CREATE TABLE held_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alias_id uuid NOT NULL REFERENCES aliases (id) ON DELETE CASCADE,
    sender text NOT NULL,
    from_header text NOT NULL DEFAULT '',
    subject text NOT NULL DEFAULT '',
    preview text NOT NULL DEFAULT '',
    raw bytea NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    approved_at timestamptz,
    delivered text[] NOT NULL DEFAULT '{}',
    claimed_until timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX held_messages_alias_idx ON held_messages (alias_id, received_at) WHERE approved_at IS NULL;
CREATE INDEX held_messages_expires_idx ON held_messages (expires_at) WHERE approved_at IS NULL;
CREATE INDEX held_messages_delivery_idx ON held_messages (next_attempt_at) WHERE approved_at IS NOT NULL;

CREATE OR REPLACE VIEW mail_aliases AS
SELECT a.address, unnest(CASE WHEN a.moderated THEN ARRAY['list-' || a.id || '@aether-reports.internal'] ELSE a.destinations END) AS destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT mu.address, mu.address FROM mail_users mu
UNION ALL
SELECT 'dmarc-reports@' || d.name, 'reports@aether-reports.internal'
FROM domains d WHERE d.active AND d.verified_at IS NOT NULL;

CREATE OR REPLACE VIEW mail_restricted_lists AS
SELECT a.address
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND (a.accept_from <> 'anyone' OR a.moderated) AND d.active AND d.verified_at IS NOT NULL;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_aliases, mail_restricted_lists TO aether_mail;
    END IF;
END $$;

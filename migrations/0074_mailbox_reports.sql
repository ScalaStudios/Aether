ALTER TABLE mailboxes
    ADD COLUMN storage_bytes bigint NOT NULL DEFAULT 0 CHECK (storage_bytes >= 0),
    ADD COLUMN message_count bigint NOT NULL DEFAULT 0 CHECK (message_count >= 0),
    ADD COLUMN storage_updated_at timestamptz,
    ADD COLUMN last_web_login_at timestamptz,
    ADD COLUMN last_imap_login_at timestamptz,
    ADD COLUMN last_smtp_login_at timestamptz;

CREATE INDEX mailboxes_org_storage_idx ON mailboxes (org_id, storage_bytes DESC);

CREATE TABLE mailbox_daily_stats (
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    day date NOT NULL,
    sent_count bigint NOT NULL DEFAULT 0 CHECK (sent_count >= 0),
    received_count bigint NOT NULL DEFAULT 0 CHECK (received_count >= 0),
    sent_bytes bigint NOT NULL DEFAULT 0 CHECK (sent_bytes >= 0),
    received_bytes bigint NOT NULL DEFAULT 0 CHECK (received_bytes >= 0),
    PRIMARY KEY (mailbox_id, day)
);

CREATE INDEX mailbox_daily_stats_org_day_idx ON mailbox_daily_stats (org_id, day);

UPDATE mailboxes m
SET last_web_login_at = s.last_at
FROM (
    SELECT user_id, max(last_at) AS last_at
    FROM (
        SELECT user_id, greatest(created_at, last_seen_at) AS last_at FROM sessions
        UNION ALL
        SELECT user_id, last_login_at AS last_at FROM user_identities WHERE last_login_at IS NOT NULL
    ) src
    GROUP BY user_id
) s
WHERE m.owner_user_id = s.user_id AND m.last_web_login_at IS NULL;

CREATE OR REPLACE FUNCTION aether_mail_login(login text, hash text, login_session text, login_service text) RETURNS TABLE (address text)
LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    WITH found AS (
        SELECT c.address, p.user_id, m.id AS mailbox_id, p.user_id IS DISTINCT FROM m.owner_user_id AS delegated,
               coalesce(s.enabled AND s.log_owner_deletes, false) AS owner_deletes
        FROM mail_credentials c
        JOIN app_passwords p ON p.token_hash = c.token_hash
        JOIN mailboxes m ON m.address = c.address
        LEFT JOIN mailbox_audit_settings s ON s.org_id = m.org_id
        WHERE c.address = login AND c.token_hash = hash AND login_service = ANY(c.services)
        LIMIT 1
    ), recorded AS (
        INSERT INTO mail_sessions (session, user_id, mailbox_id, client)
        SELECT login_session, user_id, mailbox_id, 'app_password' FROM found WHERE (delegated OR owner_deletes) AND login_session <> ''
        ON CONFLICT DO NOTHING
    ), touched AS (
        UPDATE mailboxes SET
            last_imap_login_at = CASE WHEN login_service = 'imap' THEN now() ELSE last_imap_login_at END,
            last_smtp_login_at = CASE WHEN login_service = 'smtp' THEN now() ELSE last_smtp_login_at END
        FROM found f WHERE mailboxes.id = f.mailbox_id AND login_service IN ('imap', 'smtp')
    )
    SELECT CASE WHEN f.delegated AND login_service = 'smtp' THEN coalesce((
        SELECT ms.login FROM mail_senders ms JOIN mailboxes own ON own.address = ms.login AND own.owner_user_id = f.user_id
        WHERE ms.sender = f.address ORDER BY ms.login LIMIT 1), f.address) ELSE f.address END
    FROM found f
$$;

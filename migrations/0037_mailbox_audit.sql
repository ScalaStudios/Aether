CREATE TABLE mailbox_audit_settings (
    org_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    log_reads boolean NOT NULL DEFAULT true,
    log_owner_deletes boolean NOT NULL DEFAULT false,
    owners_can_view boolean NOT NULL DEFAULT false,
    retention_days integer NOT NULL DEFAULT 180 CHECK (retention_days BETWEEN 1 AND 3650),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mailbox_access_events (
    id bigserial PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    mailbox_id uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    mailbox_address text NOT NULL,
    actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    actor_email text NOT NULL,
    actor_role text NOT NULL CHECK (actor_role IN ('owner', 'delegate', 'member', 'admin')),
    action text NOT NULL,
    folder text NOT NULL DEFAULT '',
    subject text NOT NULL DEFAULT '',
    message_id text NOT NULL DEFAULT '',
    client text NOT NULL CHECK (client IN ('web', 'imap', 'app_password', 'token', 'smtp')),
    ip text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'
);

CREATE INDEX mailbox_access_events_mailbox_idx ON mailbox_access_events (mailbox_id, id DESC);
CREATE INDEX mailbox_access_events_org_idx ON mailbox_access_events (org_id, created_at);

CREATE TABLE mail_sessions (
    session text PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    client text NOT NULL CHECK (client IN ('app_password', 'token')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mail_sessions_created_idx ON mail_sessions (created_at);

CREATE FUNCTION aether_mail_login(login text, hash text, login_session text, login_service text) RETURNS TABLE (address text)
LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    WITH found AS (
        SELECT c.address, p.user_id, m.id AS mailbox_id, p.user_id IS DISTINCT FROM m.owner_user_id AS delegated,
               coalesce(s.enabled AND s.log_owner_deletes, false) AS owner_deletes
        FROM mail_credentials c
        JOIN app_passwords p ON p.token_hash = c.token_hash
        JOIN mailboxes m ON m.address = c.address
        LEFT JOIN mailbox_audit_settings s ON s.org_id = m.org_id
        WHERE c.address = login AND c.token_hash = hash
        LIMIT 1
    ), recorded AS (
        INSERT INTO mail_sessions (session, user_id, mailbox_id, client)
        SELECT login_session, user_id, mailbox_id, 'app_password' FROM found WHERE (delegated OR owner_deletes) AND login_session <> ''
        ON CONFLICT DO NOTHING
    )
    SELECT CASE WHEN f.delegated AND login_service = 'smtp' THEN coalesce((
        SELECT ms.login FROM mail_senders ms JOIN mailboxes own ON own.address = ms.login AND own.owner_user_id = f.user_id
        WHERE ms.sender = f.address ORDER BY ms.login LIMIT 1), f.address) ELSE f.address END
    FROM found f
$$;

REVOKE ALL ON FUNCTION aether_mail_login(text, text, text, text) FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT EXECUTE ON FUNCTION aether_mail_login(text, text, text, text) TO aether_mail;
    END IF;
END $$;

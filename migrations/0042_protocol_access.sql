ALTER TABLE organizations
    ADD COLUMN mailbox_protocols text[] NOT NULL DEFAULT ARRAY['imap', 'smtp', 'sieve', 'dav', 'webmail']
        CHECK (mailbox_protocols <@ ARRAY['imap', 'smtp', 'sieve', 'dav', 'webmail']);

ALTER TABLE mailboxes
    ADD COLUMN protocols text[] NOT NULL DEFAULT ARRAY['imap', 'smtp', 'sieve', 'dav', 'webmail']
        CHECK (protocols <@ ARRAY['imap', 'smtp', 'sieve', 'dav', 'webmail']);

ALTER TABLE app_passwords
    ADD COLUMN scope text NOT NULL DEFAULT 'all' CHECK (scope IN ('all', 'mail', 'dav'));

CREATE OR REPLACE VIEW mail_credentials AS
SELECT m.address, p.token_hash,
       ARRAY(SELECT s FROM unnest(m.protocols) AS s
             WHERE s <> 'webmail' AND (p.scope = 'all' OR (p.scope = 'mail' AND s IN ('imap', 'smtp')) OR (p.scope = 'dav' AND s = 'dav'))) AS services
FROM app_passwords p
JOIN mailboxes m ON m.id = p.mailbox_id
JOIN domains d ON d.id = m.domain_id
JOIN users u ON u.id = p.user_id
WHERE p.revoked_at IS NULL AND m.active AND m.kind = 'user' AND d.active AND d.verified_at IS NOT NULL
  AND u.status = 'active'
  AND (m.owner_user_id = u.id OR EXISTS (SELECT 1 FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = u.id AND md.access = 'full' AND md.send_as));

CREATE OR REPLACE VIEW mail_senders AS
SELECT mu.address AS sender, mu.address AS login FROM mail_users mu
UNION
SELECT a.address, dest.destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
CROSS JOIN LATERAL unnest(CASE WHEN a.kind = 'list' THEN a.send_as ELSE a.destinations END) AS dest(destination)
JOIN mail_users mu ON mu.address = dest.destination
WHERE a.active AND d.active AND d.verified_at IS NOT NULL AND a.local_part <> ''
UNION
SELECT mu.address, lu.address
FROM mailbox_delegates md
JOIN mailboxes m ON m.id = md.mailbox_id
JOIN mail_users mu ON mu.address = m.address
JOIN users u ON u.id = md.user_id AND u.status = 'active'
JOIN mailboxes l ON l.owner_user_id = u.id AND l.kind = 'user'
JOIN mail_users lu ON lu.address = l.address
WHERE md.send_as AND 'smtp' = ANY(m.protocols);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_credentials, mail_senders TO aether_mail;
    END IF;
END $$;

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
    )
    SELECT CASE WHEN f.delegated AND login_service = 'smtp' THEN coalesce((
        SELECT ms.login FROM mail_senders ms JOIN mailboxes own ON own.address = ms.login AND own.owner_user_id = f.user_id
        WHERE ms.sender = f.address ORDER BY ms.login LIMIT 1), f.address) ELSE f.address END
    FROM found f
$$;

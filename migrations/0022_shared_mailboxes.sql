ALTER TABLE mailboxes
    ADD COLUMN kind text NOT NULL DEFAULT 'user' CHECK (kind IN ('user', 'shared')),
    ADD COLUMN acl_pending boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT mailboxes_shared_unowned CHECK (kind = 'user' OR owner_user_id IS NULL);

CREATE INDEX mailboxes_acl_pending_idx ON mailboxes (id) WHERE acl_pending;

ALTER TABLE mailbox_delegates
    ADD COLUMN send_as boolean NOT NULL DEFAULT false,
    ADD COLUMN send_on_behalf boolean NOT NULL DEFAULT false;

UPDATE mailbox_delegates SET send_as = true WHERE access = 'full';

ALTER TABLE scheduled_messages ADD COLUMN sender_user_id uuid REFERENCES users (id) ON DELETE SET NULL;

CREATE OR REPLACE VIEW mail_credentials AS
SELECT m.address, p.token_hash
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
WHERE md.send_as;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_credentials, mail_senders TO aether_mail;
    END IF;
END $$;

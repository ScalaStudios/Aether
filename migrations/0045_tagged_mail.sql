ALTER TABLE mailboxes ADD COLUMN tagged_mail text NOT NULL DEFAULT 'inbox' CHECK (tagged_mail IN ('inbox', 'create_folder', 'existing_folder', 'reject'));

CREATE OR REPLACE VIEW mail_users AS
SELECT m.id, m.address, m.quota_bytes, m.tagged_mail
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
WHERE m.active AND d.active AND d.verified_at IS NOT NULL;

CREATE OR REPLACE VIEW mail_userdb AS
SELECT m.id, m.address, m.quota_bytes, EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = m.id) AS held, m.tagged_mail
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
WHERE (m.active AND d.active AND d.verified_at IS NOT NULL)
   OR EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = m.id);

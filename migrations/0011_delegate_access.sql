ALTER TABLE mailbox_delegates ADD COLUMN access text NOT NULL DEFAULT 'full' CHECK (access IN ('read', 'full'));

CREATE OR REPLACE VIEW mail_credentials AS
SELECT m.address, p.token_hash
FROM app_passwords p
JOIN mailboxes m ON m.id = p.mailbox_id
JOIN domains d ON d.id = m.domain_id
JOIN users u ON u.id = p.user_id
WHERE p.revoked_at IS NULL AND m.active AND d.active AND d.verified_at IS NOT NULL
  AND u.status = 'active'
  AND (m.owner_user_id = u.id OR EXISTS (SELECT 1 FROM mailbox_delegates md WHERE md.mailbox_id = m.id AND md.user_id = u.id AND md.access = 'full'));

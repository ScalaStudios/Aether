CREATE OR REPLACE VIEW mail_userdb AS
SELECT m.id, m.address, m.quota_bytes, EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = m.id) AS held, m.tagged_mail,
       coalesce((SELECT max(r.days) FROM retention_policies r WHERE r.action = 'keep' AND r.org_id = m.org_id AND (r.mailbox_id IS NULL OR r.mailbox_id = m.id)), 0) AS keep_days
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
WHERE (d.active AND d.verified_at IS NOT NULL AND (m.active OR m.purge_after IS NOT NULL))
   OR EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = m.id);

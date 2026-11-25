INSERT INTO sender_list_entries (org_id, mailbox_id, action, kind, value, created_by, created_at)
SELECT m.org_id, a.mailbox_id, 'allow', 'address', a.sender, a.created_by, a.created_at
FROM quarantine_allowed_senders a
JOIN mailboxes m ON m.id = a.mailbox_id
WHERE char_length(a.sender) <= 320
ON CONFLICT (org_id, domain_id, mailbox_id, value) DO NOTHING;

DROP TABLE quarantine_allowed_senders;

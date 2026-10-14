ALTER TABLE mailbox_rules ADD COLUMN auto_reply jsonb NOT NULL DEFAULT '{"enabled": false, "subject": "", "message": ""}';

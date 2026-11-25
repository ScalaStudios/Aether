ALTER TABLE mailbox_signatures
    ADD COLUMN from_address text NOT NULL DEFAULT '',
    DROP CONSTRAINT mailbox_signatures_pkey,
    ADD PRIMARY KEY (mailbox_id, from_address);

ALTER TABLE scheduled_messages ADD COLUMN from_address text;

ALTER TABLE mailboxes ADD UNIQUE (id, org_id);
ALTER TABLE aliases
    ADD COLUMN sent_mailbox_id uuid,
    ADD FOREIGN KEY (sent_mailbox_id, org_id) REFERENCES mailboxes (id, org_id) ON DELETE SET NULL (sent_mailbox_id),
    ADD CHECK (kind = 'list' OR sent_mailbox_id IS NULL);

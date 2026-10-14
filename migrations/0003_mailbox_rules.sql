CREATE TABLE mailbox_rules (
    mailbox_id uuid PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,
    rules jsonb NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL DEFAULT now()
);

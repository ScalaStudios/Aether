CREATE TABLE mailbox_signatures (
    mailbox_id uuid PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,
    settings jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

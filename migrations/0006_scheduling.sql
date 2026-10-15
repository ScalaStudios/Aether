CREATE TABLE scheduled_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    send_at timestamptz NOT NULL,
    recipients text[] NOT NULL,
    bcc text[] NOT NULL DEFAULT '{}',
    to_addresses jsonb NOT NULL DEFAULT '[]',
    subject text NOT NULL DEFAULT '',
    in_reply_to text NOT NULL DEFAULT '',
    raw bytea NOT NULL,
    attempts int NOT NULL DEFAULT 0,
    error text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX scheduled_messages_due_idx ON scheduled_messages (send_at) WHERE error IS NULL;
CREATE INDEX scheduled_messages_mailbox_idx ON scheduled_messages (mailbox_id, send_at);

CREATE TABLE snoozed_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    uid_validity bigint NOT NULL,
    uid bigint NOT NULL,
    return_folder text NOT NULL,
    until timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (mailbox_id, uid_validity, uid)
);

CREATE INDEX snoozed_messages_due_idx ON snoozed_messages (until);

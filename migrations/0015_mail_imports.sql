CREATE TABLE mail_imports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    host text NOT NULL,
    username text NOT NULL,
    status text NOT NULL DEFAULT 'running',
    progress jsonb NOT NULL DEFAULT '{}',
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE INDEX mail_imports_mailbox ON mail_imports (mailbox_id, created_at DESC);

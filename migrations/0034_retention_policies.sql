CREATE TABLE retention_policies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    mailbox_id uuid REFERENCES mailboxes (id) ON DELETE CASCADE,
    action text NOT NULL CHECK (action IN ('delete', 'keep')),
    folder text NOT NULL DEFAULT '' CHECK (char_length(folder) <= 200),
    days integer CHECK (days BETWEEN 1 AND 36500),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((action = 'keep') = (folder = '')),
    CHECK (action = 'delete' OR days IS NOT NULL),
    UNIQUE NULLS NOT DISTINCT (org_id, mailbox_id, action, folder)
);

CREATE INDEX retention_policies_mailbox_idx ON retention_policies (mailbox_id) WHERE mailbox_id IS NOT NULL;

CREATE TABLE retention_runs (
    mailbox_id uuid PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,
    next_run_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz
);

CREATE INDEX retention_runs_due_idx ON retention_runs (next_run_at);

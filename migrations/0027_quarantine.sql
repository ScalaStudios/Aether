CREATE TABLE quarantine_policies (
    org_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    access jsonb NOT NULL DEFAULT '{}',
    retention_days integer NOT NULL DEFAULT 30 CHECK (retention_days BETWEEN 1 AND 90),
    digest text NOT NULL DEFAULT 'daily' CHECK (digest IN ('off', 'daily', 'weekly')),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE quarantine_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    raw bytea NOT NULL,
    size integer NOT NULL,
    category text NOT NULL CHECK (category IN ('spam', 'high_confidence_spam', 'phishing', 'malware')),
    action text NOT NULL,
    score real NOT NULL,
    symbols jsonb NOT NULL DEFAULT '[]',
    envelope_from text NOT NULL,
    from_address text NOT NULL,
    from_name text NOT NULL,
    subject text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE quarantine_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id uuid NOT NULL REFERENCES quarantine_messages (id) ON DELETE CASCADE,
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    recipient text NOT NULL,
    status text NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'requested', 'released')),
    requested_at timestamptz,
    requested_by uuid REFERENCES users (id) ON DELETE SET NULL,
    released_at timestamptz,
    released_by uuid REFERENCES users (id) ON DELETE SET NULL,
    notified_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (message_id, mailbox_id)
);

CREATE INDEX quarantine_items_org_idx ON quarantine_items (org_id, created_at DESC);
CREATE INDEX quarantine_items_mailbox_idx ON quarantine_items (mailbox_id, created_at DESC);
CREATE INDEX quarantine_items_digest_idx ON quarantine_items (mailbox_id) WHERE notified_at IS NULL AND status = 'held';

CREATE TABLE quarantine_allowed_senders (
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    sender text NOT NULL CHECK (sender = lower(sender) AND sender LIKE '_%@_%'),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mailbox_id, sender)
);

CREATE TABLE sender_list_entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    domain_id uuid REFERENCES domains (id) ON DELETE CASCADE,
    mailbox_id uuid REFERENCES mailboxes (id) ON DELETE CASCADE,
    action text NOT NULL CHECK (action IN ('allow', 'block')),
    kind text NOT NULL CHECK (kind IN ('address', 'domain', 'ip')),
    value text NOT NULL CHECK (value = lower(value) AND char_length(value) BETWEEN 1 AND 320),
    note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    expires_at timestamptz,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (domain_id IS NULL OR mailbox_id IS NULL),
    CHECK (mailbox_id IS NULL OR (kind <> 'ip' AND expires_at IS NULL)),
    UNIQUE NULLS NOT DISTINCT (org_id, domain_id, mailbox_id, value)
);

CREATE INDEX sender_list_entries_value_idx ON sender_list_entries (value);
CREATE INDEX sender_list_entries_mailbox_idx ON sender_list_entries (mailbox_id) WHERE mailbox_id IS NOT NULL;
CREATE INDEX sender_list_entries_ip_idx ON sender_list_entries (org_id) WHERE kind = 'ip';

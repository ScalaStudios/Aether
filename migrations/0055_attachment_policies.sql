CREATE TABLE organization_attachment_policies (
    org_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    inbound_action text NOT NULL CHECK (inbound_action IN ('off', 'reject', 'remove')),
    inbound_extensions text[] NOT NULL CHECK (cardinality(inbound_extensions) <= 300),
    outbound_action text NOT NULL CHECK (outbound_action IN ('off', 'reject', 'remove')),
    outbound_extensions text[] NOT NULL CHECK (cardinality(outbound_extensions) <= 300),
    exempt_senders text[] NOT NULL DEFAULT '{}' CHECK (cardinality(exempt_senders) <= 500),
    updated_at timestamptz NOT NULL DEFAULT now()
);

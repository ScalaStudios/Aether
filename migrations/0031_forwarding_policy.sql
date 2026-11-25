CREATE TABLE organization_forwarding (
    org_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    mode text NOT NULL DEFAULT 'allowed' CHECK (mode IN ('allowed', 'domains', 'blocked')),
    domains text[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now()
);

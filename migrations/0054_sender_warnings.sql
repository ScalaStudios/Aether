CREATE TABLE organization_sender_warnings (
    org_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    external_tag boolean NOT NULL DEFAULT true,
    first_contact boolean NOT NULL DEFAULT true,
    impersonation boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now()
);

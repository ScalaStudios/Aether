ALTER TABLE mailboxes ADD COLUMN hidden_from_directory boolean NOT NULL DEFAULT false;

ALTER TABLE aliases ADD COLUMN hidden_from_directory boolean NOT NULL DEFAULT false;

CREATE TABLE organization_contacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    email text NOT NULL CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
    company text NOT NULL DEFAULT '' CHECK (length(company) <= 200),
    phone text NOT NULL DEFAULT '' CHECK (length(phone) <= 50),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, email)
);

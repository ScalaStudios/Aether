CREATE TABLE signature_templates (
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    domain_id uuid REFERENCES domains (id) ON DELETE CASCADE,
    html text NOT NULL CHECK (octet_length(html) <= 32768),
    links jsonb NOT NULL DEFAULT '[]',
    logo_width integer NOT NULL DEFAULT 64 CHECK (logo_width BETWEEN 16 AND 240),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (org_id, domain_id)
);

ALTER TABLE users ADD COLUMN title text NOT NULL DEFAULT '' CHECK (char_length(title) <= 100);
ALTER TABLE users ADD COLUMN phone text NOT NULL DEFAULT '' CHECK (char_length(phone) <= 40);

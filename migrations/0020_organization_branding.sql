CREATE TABLE organization_branding (
    org_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    name text NOT NULL DEFAULT '' CHECK (char_length(name) <= 100),
    sign_in_message text NOT NULL DEFAULT '' CHECK (char_length(sign_in_message) <= 500),
    colors jsonb NOT NULL DEFAULT '{}',
    custom_css text NOT NULL DEFAULT '' CHECK (octet_length(custom_css) <= 32768),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE organization_brand_images (
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('logo-light', 'logo-dark', 'favicon')),
    content_type text NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg', 'image/webp')),
    data bytea NOT NULL CHECK (octet_length(data) <= 262144),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, kind)
);

CREATE TABLE suppressions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    address text NOT NULL CHECK (address = lower(address) AND length(address) <= 320 AND address LIKE '%_@_%'),
    reason text NOT NULL CHECK (reason IN ('bounce', 'complaint', 'manual')),
    detail text,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, address)
);

CREATE TABLE mail_log_cursors (
    path text PRIMARY KEY,
    inode bigint NOT NULL,
    "offset" bigint NOT NULL,
    queue jsonb NOT NULL
);

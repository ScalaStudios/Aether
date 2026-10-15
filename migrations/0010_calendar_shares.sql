CREATE TABLE calendar_shares (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_id uuid NOT NULL REFERENCES dav_collections (id) ON DELETE CASCADE,
    user_id uuid REFERENCES users (id) ON DELETE CASCADE,
    org_id uuid REFERENCES organizations (id) ON DELETE CASCADE,
    access text NOT NULL CHECK (access IN ('read', 'write')),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((user_id IS NULL) <> (org_id IS NULL))
);

CREATE UNIQUE INDEX calendar_shares_user_key ON calendar_shares (collection_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX calendar_shares_org_key ON calendar_shares (collection_id, org_id) WHERE org_id IS NOT NULL;
CREATE INDEX calendar_shares_user_idx ON calendar_shares (user_id);
CREATE INDEX calendar_shares_org_idx ON calendar_shares (org_id);

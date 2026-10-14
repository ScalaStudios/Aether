CREATE TABLE dav_collections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('calendar', 'addressbook')),
    slug text NOT NULL CHECK (slug ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    color text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, kind, slug)
);

CREATE TABLE dav_objects (
    collection_id uuid NOT NULL REFERENCES dav_collections (id) ON DELETE CASCADE,
    name text NOT NULL CHECK (name ~ '^[^/]{1,255}$'),
    uid text NOT NULL,
    etag text NOT NULL,
    data text NOT NULL,
    summary text NOT NULL DEFAULT '',
    emails text[] NOT NULL DEFAULT '{}',
    starts_at timestamptz,
    ends_at timestamptz,
    recurring boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection_id, name),
    UNIQUE (collection_id, uid)
);

CREATE INDEX dav_objects_emails_idx ON dav_objects USING gin (emails);
CREATE INDEX dav_objects_time_idx ON dav_objects (collection_id, starts_at);

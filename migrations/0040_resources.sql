ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_kind_check;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_kind_check CHECK (kind IN ('user', 'shared', 'resource'));

CREATE TABLE resources (
    mailbox_id uuid PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,
    resource_type text NOT NULL CHECK (resource_type IN ('room', 'equipment')),
    capacity integer CHECK (capacity BETWEEN 1 AND 10000),
    location text NOT NULL DEFAULT '' CHECK (length(location) <= 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    booking_policy text NOT NULL DEFAULT 'auto' CHECK (booking_policy IN ('auto', 'approval')),
    bookable_by text NOT NULL DEFAULT 'organization' CHECK (bookable_by IN ('organization', 'selected')),
    window_days integer NOT NULL DEFAULT 180 CHECK (window_days BETWEEN 1 AND 1095),
    max_minutes integer CHECK (max_minutes BETWEEN 15 AND 20160),
    working_hours boolean NOT NULL DEFAULT false,
    work_days integer[] NOT NULL DEFAULT '{1,2,3,4,5}' CHECK (cardinality(work_days) BETWEEN 1 AND 7 AND work_days <@ '{0,1,2,3,4,5,6}'),
    work_start integer NOT NULL DEFAULT 480 CHECK (work_start BETWEEN 0 AND 1439),
    work_end integer NOT NULL DEFAULT 1080 CHECK (work_end BETWEEN 1 AND 1440),
    time_zone text NOT NULL DEFAULT 'UTC' CHECK (length(time_zone) BETWEEN 1 AND 64),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (work_end > work_start)
);

CREATE TABLE resource_members (
    mailbox_id uuid NOT NULL REFERENCES resources (mailbox_id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('booker', 'approver')),
    PRIMARY KEY (mailbox_id, role, user_id)
);

CREATE INDEX resource_members_user_idx ON resource_members (user_id);

CREATE TABLE resource_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES resources (mailbox_id) ON DELETE CASCADE,
    uid text NOT NULL,
    organizer text NOT NULL,
    organizer_name text NOT NULL DEFAULT '',
    summary text NOT NULL DEFAULT '',
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    recurring boolean NOT NULL DEFAULT false,
    data text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (mailbox_id, uid)
);

CREATE INDEX resource_requests_due_idx ON resource_requests (ends_at) WHERE NOT recurring;

ALTER TABLE dav_collections
    ALTER COLUMN user_id DROP NOT NULL,
    ADD COLUMN mailbox_id uuid REFERENCES mailboxes (id) ON DELETE CASCADE,
    ADD CONSTRAINT dav_collections_one_owner CHECK ((user_id IS NULL) <> (mailbox_id IS NULL));

CREATE UNIQUE INDEX dav_collections_mailbox_key ON dav_collections (mailbox_id, kind, slug) WHERE mailbox_id IS NOT NULL;

CREATE OR REPLACE VIEW mail_restricted_lists AS
SELECT a.address
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND (a.accept_from <> 'anyone' OR a.moderated) AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT m.address
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
WHERE m.kind = 'resource' AND m.active AND d.active AND d.verified_at IS NOT NULL;

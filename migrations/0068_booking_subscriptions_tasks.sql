ALTER TABLE dav_objects
    ADD COLUMN component text NOT NULL DEFAULT 'VEVENT' CHECK (component IN ('VEVENT', 'VTODO'));

UPDATE dav_objects SET component = 'VTODO'
WHERE position('BEGIN:VTODO' in data) > 0 AND position('BEGIN:VEVENT' in data) = 0;

CREATE TABLE calendar_subscriptions (
    collection_id uuid PRIMARY KEY REFERENCES dav_collections (id) ON DELETE CASCADE,
    url text NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
    refresh_minutes int NOT NULL CHECK (refresh_minutes BETWEEN 15 AND 10080),
    etag text NOT NULL DEFAULT '',
    last_modified text NOT NULL DEFAULT '',
    last_fetched_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    next_fetch_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX calendar_subscriptions_due_idx ON calendar_subscriptions (next_fetch_at);

CREATE TABLE booking_pages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    timezone text NOT NULL CHECK (char_length(timezone) BETWEEN 1 AND 100),
    min_notice_minutes int NOT NULL DEFAULT 60 CHECK (min_notice_minutes BETWEEN 0 AND 10080),
    horizon_days int NOT NULL DEFAULT 60 CHECK (horizon_days BETWEEN 1 AND 180),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE booking_types (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    page_id uuid NOT NULL REFERENCES booking_pages (id) ON DELETE CASCADE,
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    duration_minutes int NOT NULL CHECK (duration_minutes BETWEEN 5 AND 480),
    buffer_before_minutes int NOT NULL DEFAULT 0 CHECK (buffer_before_minutes BETWEEN 0 AND 240),
    buffer_after_minutes int NOT NULL DEFAULT 0 CHECK (buffer_after_minutes BETWEEN 0 AND 240),
    location text NOT NULL DEFAULT '' CHECK (char_length(location) <= 500),
    video_url text NOT NULL DEFAULT '' CHECK (char_length(video_url) <= 2000),
    calendar_slug text NOT NULL DEFAULT 'personal',
    active boolean NOT NULL DEFAULT true,
    position int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (page_id, slug)
);

CREATE INDEX booking_types_page_idx ON booking_types (page_id, position, created_at);

CREATE TABLE booking_windows (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type_id uuid NOT NULL REFERENCES booking_types (id) ON DELETE CASCADE,
    weekday int NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    start_minute int NOT NULL CHECK (start_minute BETWEEN 0 AND 1435),
    end_minute int NOT NULL CHECK (end_minute > start_minute AND end_minute <= 1440)
);

CREATE INDEX booking_windows_type_idx ON booking_windows (type_id, weekday, start_minute);

CREATE TABLE bookings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type_id uuid NOT NULL REFERENCES booking_types (id) ON DELETE CASCADE,
    host_user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    event_uid text NOT NULL,
    event_name text NOT NULL,
    guest_name text NOT NULL,
    guest_email text NOT NULL,
    note text NOT NULL DEFAULT '',
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX bookings_host_time_idx ON bookings (host_user_id, starts_at, ends_at);

CREATE TABLE notification_settings (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    mail_enabled boolean NOT NULL DEFAULT true,
    calendar_enabled boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mail_folder_notifications (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    folder text NOT NULL CHECK (char_length(folder) BETWEEN 1 AND 255 AND position(E'\n' IN folder) = 0 AND position(E'\r' IN folder) = 0),
    enabled boolean NOT NULL,
    PRIMARY KEY (user_id, mailbox_id, folder)
);

CREATE TABLE web_push_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint text NOT NULL CHECK (char_length(endpoint) BETWEEN 12 AND 2000 AND endpoint LIKE 'https://%'),
    p256dh text NOT NULL CHECK (char_length(p256dh) BETWEEN 1 AND 200),
    auth text NOT NULL CHECK (char_length(auth) BETWEEN 1 AND 200),
    user_agent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_success_at timestamptz,
    UNIQUE (user_id, endpoint)
);

CREATE INDEX web_push_subscriptions_user_idx ON web_push_subscriptions (user_id);

CREATE TABLE ios_push_devices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    account_id text NOT NULL CHECK (char_length(account_id) BETWEEN 1 AND 128),
    device_token text NOT NULL CHECK (device_token ~ '^[0-9a-f]{64,200}$'),
    subtopic text NOT NULL CHECK (subtopic IN ('com.apple.mobilemail', 'com.apple.mail.XServer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (mailbox_id, device_token)
);

CREATE INDEX ios_push_devices_mailbox_idx ON ios_push_devices (mailbox_id);

CREATE TABLE push_credentials (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    public_key text NOT NULL,
    private_key bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_deliveries (
    dedupe_key text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notification_deliveries_created_idx ON notification_deliveries (created_at);

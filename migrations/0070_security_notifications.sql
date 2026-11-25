CREATE TABLE organization_security_settings (
    org_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    notify_app_password_created boolean NOT NULL DEFAULT true,
    notify_external_forwarding_rule boolean NOT NULL DEFAULT true,
    notify_delegate_added boolean NOT NULL DEFAULT true,
    notify_new_device_sign_in boolean NOT NULL DEFAULT true,
    notify_failed_sign_ins boolean NOT NULL DEFAULT true,
    notify_admins boolean NOT NULL DEFAULT false,
    failed_sign_in_threshold integer NOT NULL DEFAULT 5 CHECK (failed_sign_in_threshold BETWEEN 3 AND 50),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE security_notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id uuid REFERENCES organizations (id) ON DELETE SET NULL,
    mailbox_id uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    event_type text NOT NULL CHECK (event_type IN (
        'app_password_created',
        'external_forwarding_rule',
        'delegate_added',
        'new_device_sign_in',
        'failed_sign_ins'
    )),
    title text NOT NULL,
    summary text NOT NULL,
    details jsonb NOT NULL DEFAULT '{}',
    action_url text NOT NULL DEFAULT '',
    target_session_hash bytea,
    read_at timestamptz,
    undone_at timestamptz,
    reported_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX security_notifications_user_idx ON security_notifications (user_id, created_at DESC);
CREATE INDEX security_notifications_user_event_idx ON security_notifications (user_id, event_type, created_at DESC);

CREATE TABLE security_notification_emails (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    notification_id uuid REFERENCES security_notifications (id) ON DELETE CASCADE,
    recipient_user_id uuid REFERENCES users (id) ON DELETE CASCADE,
    recipient_address text NOT NULL,
    recipient_role text NOT NULL CHECK (recipient_role IN ('user', 'admin')),
    org_id uuid REFERENCES organizations (id) ON DELETE CASCADE,
    subject text NOT NULL,
    text_body text NOT NULL,
    html_body text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    attempts integer NOT NULL DEFAULT 0,
    last_error text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX security_notification_emails_pending_idx ON security_notification_emails (next_attempt_at) WHERE status = 'pending';

CREATE TABLE user_known_devices (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    device_hash text NOT NULL,
    country text NOT NULL DEFAULT '',
    device_label text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    last_ip text NOT NULL DEFAULT '',
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device_hash, country)
);

CREATE INDEX user_known_devices_user_idx ON user_known_devices (user_id);

CREATE TABLE user_failed_sign_ins (
    id bigserial PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    ip text NOT NULL DEFAULT '',
    country text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX user_failed_sign_ins_user_idx ON user_failed_sign_ins (user_id, created_at DESC);

ALTER TABLE organizations
    ADD COLUMN remote_image_policy text NOT NULL DEFAULT 'block'
        CHECK (remote_image_policy IN ('block', 'proxy'));

CREATE TABLE remote_image_senders (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    address text NOT NULL CHECK (address = lower(address) AND position('@' IN address) > 1 AND char_length(address) <= 320),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, address)
);

CREATE TABLE remote_image_messages (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    message_key text NOT NULL CHECK (char_length(message_key) BETWEEN 1 AND 1000),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, message_key)
);

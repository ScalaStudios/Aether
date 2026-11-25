ALTER TABLE organizations
    ADD COLUMN smime_policy text NOT NULL DEFAULT 'allow' CHECK (smime_policy IN ('off', 'allow', 'require')),
    ADD COLUMN openpgp_policy text NOT NULL DEFAULT 'allow' CHECK (openpgp_policy IN ('off', 'allow', 'require')),
    ADD COLUMN password_policy text NOT NULL DEFAULT 'allow' CHECK (password_policy IN ('off', 'allow', 'require'));

CREATE TABLE protected_messages (
    id uuid PRIMARY KEY,
    mailbox_id uuid NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    sender_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recipient text NOT NULL,
    access_hash bytea NOT NULL CHECK (octet_length(access_hash) = 32),
    reply_hash bytea NOT NULL CHECK (octet_length(reply_hash) = 32),
    salt bytea NOT NULL CHECK (octet_length(salt) = 16),
    message bytea NOT NULL CHECK (octet_length(message) BETWEEN 29 AND 262144),
    reply bytea CHECK (octet_length(reply) BETWEEN 29 AND 262144),
    expires_at timestamptz NOT NULL,
    sent_at timestamptz
);

CREATE INDEX protected_messages_expiry_idx ON protected_messages (expires_at);

ALTER TABLE scheduled_messages
    ADD COLUMN protected_reply_id uuid REFERENCES protected_messages(id) ON DELETE CASCADE;

CREATE INDEX scheduled_messages_protected_reply_idx ON scheduled_messages (protected_reply_id)
    WHERE protected_reply_id IS NOT NULL;

CREATE TABLE encryption_keys (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    address text NOT NULL,
    protocol text NOT NULL CHECK (protocol IN ('smime', 'openpgp')),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{40}$' OR fingerprint ~ '^[0-9a-f]{64}$'),
    public_key bytea NOT NULL CHECK (octet_length(public_key) BETWEEN 32 AND 65536),
    private_key bytea NOT NULL CHECK (octet_length(private_key) BETWEEN 29 AND 65536),
    salt bytea NOT NULL CHECK (octet_length(salt) = 16),
    iterations integer NOT NULL CHECK (iterations BETWEEN 200000 AND 600000),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, address, protocol)
);

CREATE INDEX encryption_keys_directory_idx ON encryption_keys (org_id, address);

CREATE TABLE peer_keys (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    address text NOT NULL,
    protocol text NOT NULL CHECK (protocol IN ('smime', 'openpgp')),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{40}$' OR fingerprint ~ '^[0-9a-f]{64}$'),
    public_key bytea NOT NULL CHECK (octet_length(public_key) BETWEEN 32 AND 65536),
    source text NOT NULL CHECK (source IN ('message', 'autocrypt', 'wkd')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX peer_keys_identity_idx ON peer_keys (user_id, lower(address), protocol, fingerprint);

ALTER TABLE organizations ADD COLUMN two_factor_policy text NOT NULL DEFAULT 'off' CHECK (two_factor_policy IN ('off', 'admins', 'everyone'));

ALTER TABLE sessions ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid();

CREATE UNIQUE INDEX sessions_id_idx ON sessions (id);

CREATE TABLE user_totp (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret bytea NOT NULL,
    enabled_at timestamptz,
    last_step bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_security_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    credential_id bytea NOT NULL UNIQUE,
    public_key bytea NOT NULL,
    sign_count bigint NOT NULL DEFAULT 0,
    transports text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);

CREATE INDEX user_security_keys_user_idx ON user_security_keys (user_id);

CREATE TABLE user_recovery_codes (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash bytea NOT NULL,
    PRIMARY KEY (user_id, code_hash)
);

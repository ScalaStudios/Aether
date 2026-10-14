CREATE TABLE organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE CHECK (email = lower(email) AND email LIKE '%_@_%'),
    display_name text NOT NULL DEFAULT '',
    password_hash text,
    is_platform_admin boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'deprovisioned')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE organization_members (
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

CREATE INDEX organization_members_user_idx ON organization_members (user_id);

CREATE TABLE domains (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name text NOT NULL UNIQUE CHECK (name = lower(name) AND name ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$'),
    verification_token text NOT NULL,
    verified_at timestamptz,
    dkim_selector text NOT NULL,
    dkim_private_key bytea NOT NULL,
    dkim_public_key text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX domains_org_idx ON domains (org_id);

CREATE TABLE mailboxes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    domain_id uuid NOT NULL REFERENCES domains (id) ON DELETE CASCADE,
    local_part text NOT NULL CHECK (local_part = lower(local_part) AND local_part ~ '^[a-z0-9]([a-z0-9._+-]{0,62}[a-z0-9])?$'),
    address text NOT NULL UNIQUE,
    display_name text NOT NULL DEFAULT '',
    owner_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    quota_bytes bigint NOT NULL DEFAULT 0 CHECK (quota_bytes >= 0),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part)
);

CREATE INDEX mailboxes_org_idx ON mailboxes (org_id);
CREATE INDEX mailboxes_owner_idx ON mailboxes (owner_user_id);

CREATE TABLE aliases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    domain_id uuid NOT NULL REFERENCES domains (id) ON DELETE CASCADE,
    local_part text NOT NULL CHECK (local_part = lower(local_part) AND local_part ~ '^([a-z0-9]([a-z0-9._+-]{0,62}[a-z0-9])?)?$'),
    address text NOT NULL UNIQUE,
    destinations text[] NOT NULL CHECK (cardinality(destinations) BETWEEN 1 AND 100),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part)
);

CREATE INDEX aliases_org_idx ON aliases (org_id);

CREATE TABLE app_passwords (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

CREATE INDEX app_passwords_user_idx ON app_passwords (user_id);
CREATE INDEX app_passwords_mailbox_idx ON app_passwords (mailbox_id);

CREATE TABLE oidc_providers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name text NOT NULL,
    kind text NOT NULL DEFAULT 'generic' CHECK (kind IN ('halo', 'generic')),
    issuer text NOT NULL,
    client_id text NOT NULL,
    client_secret bytea,
    scopes text[] NOT NULL DEFAULT ARRAY['openid', 'profile', 'email'],
    auto_provision boolean NOT NULL DEFAULT false,
    admin_group text NOT NULL DEFAULT '',
    group_mappings jsonb NOT NULL DEFAULT '[]',
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_identities (
    provider_id uuid NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
    subject text NOT NULL,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    PRIMARY KEY (provider_id, subject)
);

CREATE INDEX user_identities_user_idx ON user_identities (user_id);

CREATE TABLE oidc_login_states (
    state_hash bytea PRIMARY KEY,
    provider_id uuid NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
    code_verifier text NOT NULL,
    nonce text NOT NULL,
    return_to text NOT NULL DEFAULT '/',
    expires_at timestamptz NOT NULL
);

CREATE TABLE sessions (
    id_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token text NOT NULL,
    provider_id uuid REFERENCES oidc_providers (id) ON DELETE CASCADE,
    id_token text,
    ip text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE audit_events (
    id bigserial PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    org_id uuid REFERENCES organizations (id) ON DELETE CASCADE,
    action text NOT NULL,
    target_type text NOT NULL DEFAULT '',
    target_id text NOT NULL DEFAULT '',
    ip text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'
);

CREATE INDEX audit_events_org_idx ON audit_events (org_id, id DESC);

CREATE VIEW mail_domains AS
SELECT d.name
FROM domains d
WHERE d.active AND d.verified_at IS NOT NULL;

CREATE VIEW mail_users AS
SELECT m.id, m.address, m.quota_bytes
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
WHERE m.active AND d.active AND d.verified_at IS NOT NULL;

CREATE VIEW mail_credentials AS
SELECT m.address, p.token_hash
FROM app_passwords p
JOIN mailboxes m ON m.id = p.mailbox_id
JOIN domains d ON d.id = m.domain_id
JOIN users u ON u.id = p.user_id
WHERE p.revoked_at IS NULL AND m.active AND d.active AND d.verified_at IS NOT NULL
  AND u.status = 'active' AND (m.owner_user_id = u.id);

CREATE VIEW mail_aliases AS
SELECT a.address, unnest(a.destinations) AS destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT mu.address, mu.address FROM mail_users mu;

CREATE VIEW mail_senders AS
SELECT mu.address AS sender, mu.address AS login FROM mail_users mu
UNION
SELECT a.address, dest.destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
CROSS JOIN LATERAL unnest(a.destinations) AS dest(destination)
JOIN mail_users mu ON mu.address = dest.destination
WHERE a.active AND d.active AND d.verified_at IS NOT NULL AND a.local_part <> '';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_domains, mail_users, mail_credentials, mail_aliases, mail_senders TO aether_mail;
    END IF;
END $$;

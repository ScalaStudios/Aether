CREATE TABLE dkim_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_id uuid NOT NULL REFERENCES domains (id) ON DELETE CASCADE,
    selector text NOT NULL CHECK (selector ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' AND length(selector) <= 63),
    algorithm text NOT NULL CHECK (algorithm IN ('rsa', 'ed25519')),
    bits integer NOT NULL,
    private_key bytea NOT NULL,
    public_key text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'active', 'retired')),
    created_at timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz,
    retired_at timestamptz,
    UNIQUE (domain_id, selector)
);

CREATE UNIQUE INDEX dkim_keys_active_idx ON dkim_keys (domain_id, algorithm) WHERE state = 'active';
CREATE UNIQUE INDEX dkim_keys_pending_idx ON dkim_keys (domain_id, algorithm) WHERE state = 'pending';

INSERT INTO dkim_keys (id, domain_id, selector, algorithm, bits, private_key, public_key, state, created_at, activated_at)
SELECT id, id, dkim_selector, 'rsa', 2048, dkim_private_key, dkim_public_key, 'active', created_at, created_at FROM domains;

ALTER TABLE domains
    DROP COLUMN dkim_selector,
    DROP COLUMN dkim_private_key,
    DROP COLUMN dkim_public_key;

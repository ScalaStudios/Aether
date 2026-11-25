CREATE TABLE blocked_addresses (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    address text NOT NULL UNIQUE,
    protocol text NOT NULL,
    failures int NOT NULL,
    blocks int NOT NULL DEFAULT 1,
    blocked_at timestamptz NOT NULL DEFAULT now(),
    blocked_until timestamptz NOT NULL
);

CREATE INDEX blocked_addresses_until_idx ON blocked_addresses (blocked_until);

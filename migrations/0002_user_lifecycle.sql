ALTER TABLE users ADD COLUMN suspended_by text NOT NULL DEFAULT '';

ALTER TABLE oidc_providers ADD COLUMN webhook_secret bytea;

CREATE TABLE provider_webhook_events (
    provider_id uuid NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
    event_id text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_id, event_id)
);

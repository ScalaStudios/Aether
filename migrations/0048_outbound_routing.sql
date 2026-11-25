CREATE TABLE outbound_routes (
    kind text NOT NULL CHECK (kind IN ('relay', 'transport', 'tls')),
    name text NOT NULL,
    target text NOT NULL DEFAULT '',
    tls_mode text NOT NULL DEFAULT '' CHECK (tls_mode IN ('', 'none', 'may', 'encrypt', 'dane', 'verify', 'secure')),
    match_names text[] NOT NULL DEFAULT '{}',
    credentials bytea,
    sender_domain text GENERATED ALWAYS AS (CASE WHEN kind = 'relay' THEN name END) STORED
        REFERENCES domains (name) ON DELETE CASCADE,
    PRIMARY KEY (kind, name),
    CHECK (credentials IS NULL OR kind = 'relay')
);

CREATE VIEW mail_outbound_relays AS
SELECT r.name, concat(CASE WHEN credentials IS NULL THEN 'aether-outbound:' ELSE 'aether-sender-auth:' END, target) AS transport
FROM outbound_routes r JOIN mail_domains d ON d.name = r.sender_domain;

CREATE VIEW mail_outbound_transports AS
SELECT r.name, 'aether-outbound:' || target AS transport
FROM outbound_routes r
WHERE kind = 'transport' AND NOT EXISTS (SELECT 1 FROM mail_domains d WHERE d.name = r.name);

CREATE VIEW mail_outbound_tls AS
SELECT name, tls_mode, tls_mode || CASE WHEN cardinality(match_names) > 0
    THEN ' match=' || array_to_string(match_names, ':') ELSE '' END AS policy
FROM outbound_routes WHERE kind = 'tls';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_outbound_relays, mail_outbound_transports, mail_outbound_tls TO aether_mail;
    END IF;
END
$$;

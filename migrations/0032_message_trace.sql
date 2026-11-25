CREATE TABLE mail_trace_messages (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    queue_id text,
    received_at timestamptz NOT NULL,
    service text NOT NULL DEFAULT '',
    client_name text NOT NULL DEFAULT '',
    client_ip text NOT NULL DEFAULT '',
    tls text,
    sasl_username text NOT NULL DEFAULT '',
    sender text NOT NULL DEFAULT '',
    recipients text[] NOT NULL DEFAULT '{}',
    recipient_status jsonb NOT NULL DEFAULT '{}',
    origin_domain text NOT NULL DEFAULT '',
    domains text[] NOT NULL DEFAULT '{}',
    message_id text NOT NULL DEFAULT '',
    subject text NOT NULL DEFAULT '',
    size bigint,
    recipient_count int NOT NULL DEFAULT 0,
    spam_score real,
    spam boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'queued',
    removed boolean NOT NULL DEFAULT false
);

CREATE INDEX mail_trace_messages_received_idx ON mail_trace_messages (received_at);
CREATE INDEX mail_trace_messages_queue_idx ON mail_trace_messages (queue_id, id) WHERE queue_id IS NOT NULL;
CREATE INDEX mail_trace_messages_message_id_idx ON mail_trace_messages (message_id) WHERE message_id <> '';
CREATE INDEX mail_trace_messages_domains_idx ON mail_trace_messages USING gin (domains);

CREATE TABLE mail_trace_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    trace_id bigint NOT NULL REFERENCES mail_trace_messages (id) ON DELETE CASCADE,
    at timestamptz NOT NULL,
    kind text NOT NULL,
    recipient text NOT NULL DEFAULT '',
    orig_recipient text NOT NULL DEFAULT '',
    relay text NOT NULL DEFAULT '',
    dsn text NOT NULL DEFAULT '',
    detail text NOT NULL DEFAULT '',
    tls text NOT NULL DEFAULT ''
);

CREATE INDEX mail_trace_events_trace_idx ON mail_trace_events (trace_id, id);

CREATE TABLE mail_trace_sources (
    path text PRIMARY KEY,
    head text NOT NULL,
    position bigint NOT NULL,
    retention_days int NOT NULL,
    read_at timestamptz NOT NULL DEFAULT now()
);

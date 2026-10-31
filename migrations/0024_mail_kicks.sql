CREATE TABLE mail_kicks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    identity text NOT NULL,
    attempts int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mail_kicks_due_idx ON mail_kicks (next_attempt_at);

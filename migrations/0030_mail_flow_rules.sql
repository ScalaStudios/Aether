CREATE TABLE mail_flow_rules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    position integer NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    mode text NOT NULL CHECK (mode IN ('enforce', 'test', 'off')),
    conditions jsonb NOT NULL DEFAULT '[]',
    exceptions jsonb NOT NULL DEFAULT '[]',
    actions jsonb NOT NULL,
    stop boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mail_flow_rules_org_idx ON mail_flow_rules (org_id, position);

CREATE TABLE mail_flow_matches (
    id bigserial PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    rule_id uuid REFERENCES mail_flow_rules (id) ON DELETE SET NULL,
    rule_name text NOT NULL,
    test boolean NOT NULL,
    direction text NOT NULL,
    sender text NOT NULL,
    recipients text[] NOT NULL,
    subject text NOT NULL,
    actions text[] NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mail_flow_matches_org_idx ON mail_flow_matches (org_id, created_at DESC);

CREATE TABLE mail_flow_held (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    rule_id uuid REFERENCES mail_flow_rules (id) ON DELETE SET NULL,
    rule_name text NOT NULL,
    approvers text[] NOT NULL CHECK (cardinality(approvers) >= 1),
    sender text NOT NULL,
    recipients text[] NOT NULL CHECK (cardinality(recipients) >= 1),
    subject text NOT NULL,
    preview text NOT NULL,
    require_tls boolean NOT NULL DEFAULT false,
    raw bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'expired')),
    decided_by uuid REFERENCES users (id) ON DELETE SET NULL,
    decided_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mail_flow_held_pending_idx ON mail_flow_held (org_id, created_at) WHERE status = 'pending';
CREATE INDEX mail_flow_held_approvers_idx ON mail_flow_held USING gin (approvers) WHERE status = 'pending';

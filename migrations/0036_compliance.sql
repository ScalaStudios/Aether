ALTER TABLE organization_members ADD COLUMN compliance boolean NOT NULL DEFAULT false;

ALTER TABLE mailboxes ADD COLUMN retained_at timestamptz;

CREATE TABLE compliance_cases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    criteria jsonb NOT NULL DEFAULT '{}',
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz
);

CREATE INDEX compliance_cases_org_idx ON compliance_cases (org_id, created_at DESC);

CREATE TABLE mailbox_holds (
    case_id uuid NOT NULL REFERENCES compliance_cases (id) ON DELETE RESTRICT,
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE RESTRICT,
    criteria jsonb,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (case_id, mailbox_id)
);

CREATE INDEX mailbox_holds_mailbox_idx ON mailbox_holds (mailbox_id);

CREATE TABLE journal_rules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    domain_id uuid NOT NULL REFERENCES domains (id) ON DELETE CASCADE,
    match text NOT NULL UNIQUE CHECK (match = lower(match) AND match LIKE '%@_%'),
    archive_address text NOT NULL CHECK (archive_address = lower(archive_address) AND archive_address LIKE '%_@_%'),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX journal_rules_org_idx ON journal_rules (org_id);

CREATE VIEW mail_userdb AS
SELECT m.id, m.address, m.quota_bytes, EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = m.id) AS held
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
WHERE (m.active AND d.active AND d.verified_at IS NOT NULL)
   OR EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = m.id);

CREATE VIEW mail_journal AS
SELECT j.match, j.archive_address
FROM journal_rules j
JOIN domains d ON d.id = j.domain_id
WHERE d.active AND d.verified_at IS NOT NULL;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_userdb, mail_journal TO aether_mail;
    END IF;
END $$;

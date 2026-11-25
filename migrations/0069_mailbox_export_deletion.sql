ALTER TABLE mailboxes
    ADD COLUMN purge_after timestamptz,
    ADD COLUMN on_hold boolean NOT NULL DEFAULT false;

CREATE INDEX mailboxes_purge_after_idx ON mailboxes (purge_after) WHERE purge_after IS NOT NULL;

CREATE TABLE mail_purges (
    mailbox_id uuid PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mailbox_exports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    format text NOT NULL CHECK (format IN ('zip', 'mbox')),
    folder text NOT NULL DEFAULT '',
    since timestamptz,
    until timestamptz,
    include_calendar boolean NOT NULL DEFAULT true,
    include_contacts boolean NOT NULL DEFAULT true,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'ready', 'failed', 'expired')),
    error text NOT NULL DEFAULT '',
    bytes bigint NOT NULL DEFAULT 0,
    messages integer NOT NULL DEFAULT 0,
    archive_oid oid,
    media_type text NOT NULL DEFAULT 'application/zip',
    filename text NOT NULL DEFAULT '',
    claim_token uuid,
    heartbeat_at timestamptz,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE INDEX mailbox_exports_mailbox_idx ON mailbox_exports (mailbox_id, created_at DESC);
CREATE INDEX mailbox_exports_pending_idx ON mailbox_exports (created_at) WHERE status = 'pending';
CREATE INDEX mailbox_exports_ready_idx ON mailbox_exports (expires_at) WHERE status = 'ready';

CREATE OR REPLACE VIEW mail_userdb AS
SELECT m.id, m.address, m.quota_bytes, EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = m.id) AS held, m.tagged_mail
FROM mailboxes m
JOIN domains d ON d.id = m.domain_id
WHERE (d.active AND d.verified_at IS NOT NULL AND (m.active OR m.purge_after IS NOT NULL))
   OR EXISTS (SELECT 1 FROM mailbox_holds h WHERE h.mailbox_id = m.id);

ALTER TABLE organizations ADD COLUMN sending_limits jsonb NOT NULL DEFAULT '{}';
ALTER TABLE domains ADD COLUMN sending_limits jsonb NOT NULL DEFAULT '{}';
ALTER TABLE mailboxes
    ADD COLUMN sending_limits jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN sending_restricted_at timestamptz,
    ADD COLUMN sending_restriction text;

CREATE INDEX mailboxes_sending_restricted_idx ON mailboxes (org_id) WHERE sending_restricted_at IS NOT NULL;

CREATE TABLE sending_recipients (
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    recipient text NOT NULL,
    first_sent_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mailbox_id, recipient)
);

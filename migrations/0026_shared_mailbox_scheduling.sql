ALTER TABLE mailboxes
    ADD COLUMN acl_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN acl_next_attempt_at timestamptz NOT NULL DEFAULT now(),
    DROP CONSTRAINT mailboxes_domain_id_fkey,
    ADD CONSTRAINT mailboxes_domain_id_fkey FOREIGN KEY (domain_id) REFERENCES domains (id) ON DELETE RESTRICT,
    DROP CONSTRAINT mailboxes_org_id_fkey,
    ADD CONSTRAINT mailboxes_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations (id) ON DELETE RESTRICT;

DROP INDEX mailboxes_acl_pending_idx;
CREATE INDEX mailboxes_acl_pending_idx ON mailboxes (acl_next_attempt_at) WHERE acl_pending;

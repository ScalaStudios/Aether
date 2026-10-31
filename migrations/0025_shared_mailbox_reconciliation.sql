ALTER TABLE users ADD CONSTRAINT users_email_exact CHECK (email !~ '[*?]');

DELETE FROM mail_kicks WHERE identity ~ '[*?]';
DELETE FROM mail_kicks a USING mail_kicks b WHERE a.identity = b.identity AND (a.created_at, a.id) > (b.created_at, b.id);

ALTER TABLE mail_kicks
    ADD COLUMN generation bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT mail_kicks_identity_key UNIQUE (identity),
    ADD CONSTRAINT mail_kicks_identity_exact CHECK (identity !~ '[*?]');

ALTER TABLE mailboxes
    ADD COLUMN acl_generation bigint NOT NULL DEFAULT 0,
    ADD COLUMN acl_pending_since timestamptz,
    ADD COLUMN acl_lease_token uuid,
    ADD COLUMN acl_lease_until timestamptz;

UPDATE mailboxes SET acl_pending_since = now() WHERE acl_pending;

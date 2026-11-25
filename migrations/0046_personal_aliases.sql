ALTER TABLE organizations ADD COLUMN personal_alias_limit integer NOT NULL DEFAULT 0 CHECK (personal_alias_limit >= 0);
ALTER TABLE domains ADD COLUMN allow_personal_aliases boolean NOT NULL DEFAULT false;

ALTER TABLE aliases
    ADD COLUMN personal_owner_id uuid REFERENCES users (id) ON DELETE CASCADE,
    ADD COLUMN personal_mailbox_id uuid REFERENCES mailboxes (id) ON DELETE CASCADE,
    ADD COLUMN note text NOT NULL DEFAULT '',
    ADD COLUMN expires_at timestamptz,
    ADD CHECK ((personal_owner_id IS NULL) = (personal_mailbox_id IS NULL)),
    ADD CHECK (personal_owner_id IS NULL OR (kind = 'alias' AND local_part <> '' AND cardinality(destinations) = 1));

CREATE INDEX aliases_personal_owner_idx ON aliases (org_id, personal_owner_id) WHERE personal_owner_id IS NOT NULL;

CREATE TABLE personal_alias_keys (
    org_id uuid NOT NULL,
    user_id uuid NOT NULL,
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    domain_id uuid NOT NULL REFERENCES domains (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    PRIMARY KEY (org_id, user_id),
    FOREIGN KEY (org_id, user_id) REFERENCES organization_members (org_id, user_id) ON DELETE CASCADE
);

CREATE FUNCTION check_personal_alias_address() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.address, 32));
    IF TG_TABLE_NAME = 'mailboxes' THEN
        IF EXISTS (SELECT FROM aliases WHERE address = NEW.address AND personal_owner_id IS NOT NULL) THEN
            RAISE unique_violation USING MESSAGE = 'Address belongs to a personal alias';
        END IF;
    ELSIF EXISTS (SELECT FROM mailboxes WHERE address = NEW.address) THEN
        RAISE unique_violation USING MESSAGE = 'Address belongs to a mailbox';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER personal_alias_address BEFORE INSERT ON aliases FOR EACH ROW
    WHEN (NEW.personal_owner_id IS NOT NULL) EXECUTE FUNCTION check_personal_alias_address();
CREATE TRIGGER mailbox_personal_alias_address BEFORE INSERT ON mailboxes FOR EACH ROW
    EXECUTE FUNCTION check_personal_alias_address();

CREATE VIEW mail_unavailable_personal_aliases AS
SELECT a.address FROM aliases a
WHERE a.personal_owner_id IS NOT NULL AND (
    NOT a.active OR a.expires_at <= now() OR NOT EXISTS (
        SELECT FROM mailboxes m
        JOIN mail_users mu ON mu.id = m.id
        JOIN organization_members om ON om.org_id = m.org_id AND om.user_id = m.owner_user_id
        JOIN users u ON u.id = om.user_id AND u.status = 'active'
        WHERE m.id = a.personal_mailbox_id AND m.owner_user_id = a.personal_owner_id AND m.kind = 'user'
    )
);

ALTER VIEW mail_aliases RENAME TO mail_alias_routes;
CREATE VIEW mail_aliases AS
SELECT r.* FROM mail_alias_routes r
WHERE NOT EXISTS (SELECT FROM mail_unavailable_personal_aliases a WHERE a.address = r.address);

ALTER VIEW mail_senders RENAME TO mail_sender_grants;
CREATE VIEW mail_senders AS
SELECT g.* FROM mail_sender_grants g
WHERE NOT EXISTS (SELECT FROM mail_unavailable_personal_aliases a WHERE a.address = g.sender);

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'aether_mail') THEN
        GRANT SELECT ON mail_aliases, mail_senders, mail_unavailable_personal_aliases TO aether_mail;
    END IF;
END $$;

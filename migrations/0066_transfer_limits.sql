CREATE TABLE organization_transfer_limits (
    org_id uuid PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    message_bytes bigint NOT NULL DEFAULT 52428800 CHECK (message_bytes BETWEEN 1 AND 2147483647),
    attachment_bytes bigint NOT NULL DEFAULT 20971520 CHECK (attachment_bytes BETWEEN 1 AND 2147483647),
    link_threshold_bytes bigint NOT NULL DEFAULT 20971520 CHECK (link_threshold_bytes BETWEEN 1 AND 2147483647),
    link_file_bytes bigint NOT NULL DEFAULT 2147483648 CHECK (link_file_bytes BETWEEN 1 AND 8589934592),
    storage_bytes bigint NOT NULL DEFAULT 10737418240 CHECK (storage_bytes BETWEEN 0 AND 109951162777600),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (attachment_bytes <= message_bytes),
    CHECK (link_threshold_bytes <= attachment_bytes),
    CHECK (link_threshold_bytes <= link_file_bytes)
);

CREATE TABLE file_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    token text NOT NULL UNIQUE CHECK (token ~ '^[A-Za-z0-9_-]{43}$'),
    filename text NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
    content_type text NOT NULL CHECK (char_length(content_type) BETWEEN 1 AND 200),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    object_id bigint NOT NULL CHECK (object_id > 0),
    password_hash text CHECK (password_hash IS NULL OR char_length(password_hash) BETWEEN 1 AND 200),
    expires_at timestamptz,
    download_count bigint NOT NULL DEFAULT 0 CHECK (download_count >= 0),
    last_download_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    committed_at timestamptz
);

CREATE INDEX file_links_org_idx ON file_links (org_id);
CREATE INDEX file_links_mailbox_idx ON file_links (mailbox_id);
CREATE INDEX file_links_expiry_idx ON file_links (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX file_links_uncommitted_idx ON file_links (created_at) WHERE committed_at IS NULL;

CREATE FUNCTION unlink_file_link_object() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM lo_unlink(OLD.object_id::oid);
    RETURN OLD;
EXCEPTION
    WHEN undefined_object THEN
        RETURN OLD;
END;
$$;

CREATE TRIGGER file_links_unlink
    BEFORE DELETE ON file_links
    FOR EACH ROW
    EXECUTE FUNCTION unlink_file_link_object();

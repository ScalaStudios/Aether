ALTER TABLE organizations
    ADD COLUMN default_quota_bytes bigint CHECK (default_quota_bytes >= 0),
    ADD COLUMN max_mailboxes bigint NOT NULL DEFAULT 0 CHECK (max_mailboxes >= 0),
    ADD COLUMN max_aliases bigint NOT NULL DEFAULT 0 CHECK (max_aliases >= 0),
    ADD COLUMN max_lists bigint NOT NULL DEFAULT 0 CHECK (max_lists >= 0),
    ADD COLUMN max_domains bigint NOT NULL DEFAULT 0 CHECK (max_domains >= 0),
    ADD COLUMN max_storage_bytes bigint NOT NULL DEFAULT 0 CHECK (max_storage_bytes >= 0);

ALTER TABLE domains ADD COLUMN default_quota_bytes bigint CHECK (default_quota_bytes >= 0);

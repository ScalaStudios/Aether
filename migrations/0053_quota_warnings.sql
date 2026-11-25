ALTER TABLE organizations
    ADD COLUMN quota_warning_percents smallint[] NOT NULL DEFAULT '{80,95}'
        CHECK (cardinality(quota_warning_percents) <= 2 AND 50 <= ALL (quota_warning_percents) AND 99 >= ALL (quota_warning_percents)),
    ADD COLUMN quota_warning_admins boolean NOT NULL DEFAULT false;

ALTER TABLE mailboxes
    ADD COLUMN used_bytes bigint NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    ADD COLUMN quota_warned_percent smallint NOT NULL DEFAULT 0;

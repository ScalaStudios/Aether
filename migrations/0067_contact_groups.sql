ALTER TABLE dav_objects
    ADD COLUMN is_group boolean NOT NULL DEFAULT false,
    ADD COLUMN member_uids text[] NOT NULL DEFAULT '{}';

CREATE INDEX dav_objects_groups_idx ON dav_objects (collection_id) WHERE is_group;
CREATE INDEX dav_objects_member_uids_idx ON dav_objects USING gin (member_uids);

ALTER TABLE organizations
    ADD COLUMN audit_retention_days int CHECK (audit_retention_days IS NULL OR (audit_retention_days >= 0 AND audit_retention_days <= 3650));

ALTER TABLE audit_events
    ADD COLUMN search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('simple',
            coalesce(action, '') || ' ' ||
            replace(coalesce(action, ''), '.', ' ') || ' ' ||
            coalesce(target_type, '') || ' ' ||
            coalesce(target_id, '') || ' ' ||
            coalesce(ip, '')
        ) || jsonb_to_tsvector('simple', coalesce(metadata, '{}'::jsonb), '["string", "numeric", "boolean"]')
    ) STORED;

CREATE INDEX audit_events_search_idx ON audit_events USING GIN (search_vector);
CREATE INDEX audit_events_org_created_idx ON audit_events (org_id, created_at DESC, id DESC);
CREATE INDEX audit_events_created_idx ON audit_events (created_at);
CREATE INDEX audit_events_org_action_idx ON audit_events (org_id, action, id DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_user_id, id DESC) WHERE actor_user_id IS NOT NULL;

ALTER TABLE webhooks ALTER COLUMN org_id DROP NOT NULL;

CREATE INDEX webhooks_platform_idx ON webhooks (id) WHERE org_id IS NULL;

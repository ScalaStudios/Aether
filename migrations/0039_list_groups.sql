CREATE TABLE directory_groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id uuid NOT NULL REFERENCES oidc_providers (id) ON DELETE CASCADE,
    external_id text NOT NULL DEFAULT '' CHECK (length(external_id) <= 256),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 256),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, display_name)
);

CREATE TABLE directory_group_members (
    group_id uuid NOT NULL REFERENCES directory_groups (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX directory_group_members_user_idx ON directory_group_members (user_id);

CREATE TABLE alias_groups (
    alias_id uuid NOT NULL REFERENCES aliases (id) ON DELETE CASCADE,
    group_id uuid NOT NULL REFERENCES directory_groups (id) ON DELETE CASCADE,
    PRIMARY KEY (alias_id, group_id)
);

CREATE INDEX alias_groups_group_idx ON alias_groups (group_id);

ALTER TABLE aliases ADD COLUMN member_domains uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(member_domains) <= 100);

ALTER TABLE aliases DROP CONSTRAINT aliases_destinations_check;
ALTER TABLE aliases ADD CONSTRAINT aliases_destinations_check
    CHECK (cardinality(destinations) >= CASE WHEN kind = 'list' THEN 0 ELSE 1 END AND cardinality(destinations) <= CASE WHEN kind = 'list' THEN 5000 ELSE 100 END);

CREATE VIEW list_dynamic_members AS
SELECT ag.alias_id, m.address, ag.group_id
FROM alias_groups ag
JOIN aliases a ON a.id = ag.alias_id AND a.kind = 'list'
JOIN directory_group_members gm ON gm.group_id = ag.group_id
JOIN users u ON u.id = gm.user_id AND u.status <> 'deprovisioned'
JOIN mailboxes m ON m.owner_user_id = u.id AND m.org_id = a.org_id AND m.kind = 'user' AND m.active
UNION
SELECT a.id, m.address, NULL::uuid
FROM aliases a
JOIN mailboxes m ON m.domain_id = ANY (a.member_domains) AND m.org_id = a.org_id AND m.kind = 'user' AND m.active
WHERE a.kind = 'list';

CREATE OR REPLACE VIEW mail_aliases AS
SELECT a.address, unnest(a.destinations) AS destination
FROM aliases a
JOIN domains d ON d.id = a.domain_id
WHERE a.active AND d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT mu.address, mu.address FROM mail_users mu
UNION ALL
SELECT 'dmarc-reports@' || d.name, 'reports@aether-reports.internal'
FROM domains d WHERE d.active AND d.verified_at IS NOT NULL
UNION ALL
SELECT DISTINCT a.address, lm.address
FROM aliases a
JOIN domains d ON d.id = a.domain_id
JOIN list_dynamic_members lm ON lm.alias_id = a.id
WHERE a.active AND d.active AND d.verified_at IS NOT NULL AND NOT lm.address = ANY (a.destinations);

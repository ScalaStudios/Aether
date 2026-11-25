ALTER TABLE domains ADD CONSTRAINT domains_id_org_unique UNIQUE (id, org_id);

CREATE TABLE organization_roles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 300),
    scopes text[] NOT NULL CHECK (cardinality(scopes) >= 1),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id)
);

CREATE UNIQUE INDEX organization_roles_org_name_idx ON organization_roles (org_id, lower(name));

ALTER TABLE organization_members
    DROP CONSTRAINT organization_members_role_check,
    ADD COLUMN custom_role_id uuid,
    ADD CONSTRAINT organization_members_role_check CHECK (role IN ('owner', 'admin', 'domain_admin', 'helpdesk', 'list_admin', 'auditor', 'custom', 'member')),
    ADD CONSTRAINT organization_members_custom_role_check CHECK ((role = 'custom') = (custom_role_id IS NOT NULL)),
    ADD CONSTRAINT organization_members_custom_role_fkey FOREIGN KEY (custom_role_id, org_id) REFERENCES organization_roles (id, org_id) ON DELETE RESTRICT;

CREATE INDEX organization_members_custom_role_idx ON organization_members (custom_role_id) WHERE custom_role_id IS NOT NULL;

CREATE TABLE organization_member_domains (
    org_id uuid NOT NULL,
    user_id uuid NOT NULL,
    domain_id uuid NOT NULL,
    PRIMARY KEY (org_id, user_id, domain_id),
    FOREIGN KEY (org_id, user_id) REFERENCES organization_members (org_id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (domain_id, org_id) REFERENCES domains (id, org_id) ON DELETE CASCADE
);

CREATE INDEX organization_member_domains_domain_idx ON organization_member_domains (domain_id);

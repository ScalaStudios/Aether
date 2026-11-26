# Roles and permissions

Every member of an organization has one organization role. Besides **Owner**, **Administrator** and **Member**, Aether includes built-in scoped administrator roles and supports custom roles composed from API key scopes, with optional per-member domain scoping.

## Built-in roles

| Role | Identifier | Scope | Capabilities |
| --- | --- | --- | --- |
| **Owner** | `owner` | Organization | Full control of the organization, including assigning and removing other owners. An organization always keeps at least one owner. |
| **Administrator** | `admin` | Organization | Full control of domains, mailboxes, aliases, distribution lists, members (except owners), custom roles, branding, signature templates, audit log, API keys and webhooks. |
| **Domain administrator** | `domain_admin` | Chosen domains | Manages mailboxes (including settings, delegates, app passwords, rules, automatic replies and calendar/contacts import), aliases, distribution lists and per-domain signature templates for the assigned domains. Read-only access to DNS status and DMARC reports for those domains. Cannot add, verify or delete domains, or edit the organization-wide signature template. |
| **Helpdesk** | `helpdesk` | Organization | Manages mailbox settings (display name, quota, active status), mail rules, automatic replies, signatures and app passwords, plus password resets and account suspension/restoration for ordinary members. Cannot read mail, manage mailbox delegates, create or delete mailboxes, or act on administrators, platform administrators or multi-organization accounts. |
| **List administrator** | `list_admin` | Organization | Lists, creates, edits and deletes distribution lists (`kind: "list"`), their members, sender restrictions, send-as rights and owners. Cannot read or modify plain aliases or convert lists into aliases. |
| **Auditor** | `auditor` | Organization | Read-only access to domains, DNS status, DMARC reports, mailboxes, delegates, app password metadata, aliases, distribution lists, members, custom roles, branding, signature templates, audit log, API keys and webhooks. Cannot modify settings or read mailbox mail, rules or automatic replies. |
| **Member** | `member` | — | Standard user with no administration console access. |

### Security boundaries

- **Helpdesk and delegation:** Helpdesk intentionally has no access to `/mailboxes/{mailboxId}/delegates`. A helpdesk operator cannot grant themselves or another person delegate access to read a user's mailbox.
- **Helpdesk and member support:** Password resets (`POST /api/v1/organizations/{orgId}/members/{userId}/password`) and account suspension/restoration (`PATCH /api/v1/organizations/{orgId}/members/{userId}/status`) enforce authority checks:
  - Platform administrators can act on any account (except suspending themselves).
  - Organization owners can act on any member of the organization who is not a platform administrator and belongs only to that organization.
  - Organization administrators can act on non-owner members who are not platform administrators and belong only to that organization.
  - Helpdesk can act only on ordinary `member` accounts who are not platform administrators and belong only to that organization.
- **No mail content access:** No administrative role grants permission to read or send a person's mail unless that administrator is separately added as a mailbox delegate.

## Custom roles

Owners and administrators can define custom roles under **Administration → Roles** (or through `POST /api/v1/organizations/{orgId}/roles`, `PUT /api/v1/roles/{roleId}`, and `DELETE /api/v1/roles/{roleId}`). API keys cannot manage custom roles.

A custom role combines the same scopes used by organization API keys:

| Scope | Grants |
| --- | --- |
| `domains:read`, `domains:write` | Viewing domains, DNS checks and DMARC reports; `write` adds verifying and deleting domains (and adding domains when not domain-scoped). |
| `mailboxes:read`, `mailboxes:write` | Viewing mailboxes, delegates and app passwords; `write` adds creating, updating, converting and deleting mailboxes, managing delegates, revoking app passwords, managing rules, automatic replies and signatures, and importing calendars and contacts. |
| `aliases:read`, `aliases:write` | Viewing aliases and distribution lists; `write` adds creating, updating and deleting both. |
| `members:read`, `members:write` | Viewing organization members; `write` adds adding, changing roles for, and removing members within the caller's own permissions. |
| `audit:read` | Reading the organization audit log. |
| `webhooks:read`, `webhooks:write` | Viewing webhooks and delivery history; `write` adds creating, testing and deleting webhooks. |

A `write` scope implies the corresponding `read` scope. Custom roles currently assigned to any member or identity provider group mapping cannot be deleted (`409 role_in_use`).

### Privilege-escalation protection

When an actor with `members:write` assigns or modifies a member's role:

- Only platform administrators and organization owners can grant or change the `owner` role.
- Only owners and full administrators can grant or change the `admin` role.
- A custom-role holder with `members:write` can only grant roles whose effective permissions are a subset of their own permissions, and if domain-scoped, can only assign domains within their own domain scope.
- Platform administrators cannot give themselves a role in an organization. They cannot add themselves as a member, raise their own role or turn on their own compliance right, through the members API, an API key, mailbox creation (including bulk import) or identity provider group mappings; the API answers `403 self_membership`. Lowering their own role or leaving is allowed. An owner or administrator of the organization who is a different person can still add or promote them.

## Domain scoping

Both `domain_admin` (required: at least one domain) and `custom` roles (optional) accept a `domainIds` list when assigning a member under **Administration → Members** (`POST /api/v1/organizations/{orgId}/members`).

For a domain-scoped administrator:

- Domain, mailbox, alias and signature-template queries are filtered to the assigned domains.
- Requests targeting a domain, mailbox or alias outside the assigned domains fail with `403`.
- Adding new domains to the organization and editing the organization-wide signature template are disallowed.
- If a `domain_admin` has all of their assigned domains removed, access fails closed (no domains are visible or manageable) until an administrator assigns another domain.

## Identity provider group mappings

Identity provider group mappings (**Administration → Identity providers**) support every built-in role, custom roles (`customRoleId`) and domain scopes (`domainIds`).

When a person signs in through an OpenID Connect provider and matches multiple group mappings for the same organization:

- Mappings only grant or upgrade access; they never downgrade an existing higher-ranked role.
- When multiple mappings grant `domain_admin` (or the same custom role) in the same organization, their `domainIds` are combined (union).
- An existing `domain_admin` or scoped custom role who matches another mapping for the same role gains any additional domains from the new mapping.

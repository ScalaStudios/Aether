import type { BuiltInRole, Member, Membership, Role, Session } from "@/lib/types"

export type ScopeAccess = "" | "read" | "write"

export const BUILT_IN_ROLES: BuiltInRole[] = [
  "owner",
  "admin",
  "domain_admin",
  "helpdesk",
  "list_admin",
  "auditor",
  "member",
]

export const ROLE_LABELS: Record<Role, string> = {
  owner: "Owner",
  admin: "Administrator",
  domain_admin: "Domain administrator",
  helpdesk: "Helpdesk",
  list_admin: "List administrator",
  auditor: "Auditor",
  custom: "Custom role",
  member: "Member",
}

export const ROLE_DESCRIPTIONS: Record<BuiltInRole, string> = {
  owner: "Full control of the organization, including managing other owners.",
  admin: "Full control of domains, mailboxes, aliases, members, custom roles, branding, signatures, audit log and integrations.",
  domain_admin: "Manages mailboxes, aliases, distribution lists and per-domain signatures for chosen domains, with read-only access to their DNS and DMARC reports.",
  helpdesk: "Manages mailbox settings, rules, automatic replies, signatures and app passwords, plus password resets and account suspension for ordinary members. No delegate access.",
  list_admin: "Creates, edits and deletes distribution lists and their membership without access to plain aliases or mailboxes.",
  auditor: "Read-only access to the administration console and audit log, without modifying settings or reading private mailbox rules.",
  member: "Standard user with no administration console access.",
}

export const SCOPE_RESOURCES: { value: string; label: string; description: string; levels: ScopeAccess[] }[] = [
  { value: "domains", label: "Domains", description: "Domains, DNS records, verification and DMARC reports.", levels: ["", "read", "write"] },
  {
    value: "mailboxes",
    label: "Mailboxes",
    description: "Mailboxes, delegates, app passwords, mail rules, automatic replies and signatures. Write also imports calendars and contacts for the mailbox owner.",
    levels: ["", "read", "write"],
  },
  { value: "aliases", label: "Aliases", description: "Aliases and distribution lists, with their members, sender restrictions and owners.", levels: ["", "read", "write"] },
  { value: "members", label: "Members", description: "Members and their roles. Write also adds, edits and removes members within your own permissions.", levels: ["", "read", "write"] },
  { value: "audit", label: "Audit log", description: "The organization’s audit log.", levels: ["", "read"] },
  { value: "webhooks", label: "Webhooks", description: "Webhooks, their delivery history and test deliveries.", levels: ["", "read", "write"] },
]

export const SCOPE_LEVEL_LABELS: Record<ScopeAccess, string> = { "": "None", read: "Read", write: "Write" }

export function orgMembership(session: Session | undefined, orgId: string): Membership | undefined {
  return session?.memberships.find((m) => m.orgId === orgId)
}

export function hasPerm(session: Session | undefined, orgId: string, ...perms: string[]): boolean {
  if (!session) return false
  if (session.user.isPlatformAdmin) return true
  const m = orgMembership(session, orgId)
  if (!m) return false
  return perms.some((p) => m.permissions.includes(p))
}

export function platformOnly(session: Session | undefined, orgId: string, ...roles: Role[]): boolean {
  const role = orgMembership(session, orgId)?.role
  return !!session?.user.isPlatformAdmin && !(role && ["owner", "admin", ...roles].includes(role))
}

export function grantHint(session: Session | undefined, members: Member[] | undefined): string {
  if (!members) return ""
  const self = session?.user.id
  if (members.some((m) => m.userId !== self && (m.role === "owner" || m.role === "admin"))) return "Ask an owner or administrator of the organization to grant you the role."
  return `The organization has no ${members.some((m) => m.userId === self && m.role === "owner") ? "other " : ""}owner yet. Add someone else as owner under Members, and they can grant you the role.`
}

export function isDomainScoped(session: Session | undefined, orgId: string): boolean {
  if (!session || session.user.isPlatformAdmin) return false
  const m = orgMembership(session, orgId)
  if (!m) return false
  return m.role === "domain_admin" || (m.role === "custom" && m.domainIds.length > 0)
}

export function roleLabel(role: Role, customRoleName?: string): string {
  if (role === "custom") return customRoleName || ROLE_LABELS.custom
  return ROLE_LABELS[role]
}

export function canReadDelegates(session: Session | undefined, orgId: string): boolean {
  if (!session) return false
  if (session.user.isPlatformAdmin) return true
  const m = orgMembership(session, orgId)
  if (!m || m.role === "helpdesk") return false
  return m.permissions.includes("mailboxes:delegates") || m.permissions.includes("mailboxes:read")
}

export function canSupportTarget(session: Session | undefined, orgId: string, target: Member): boolean {
  if (!session || target.userId === session.user.id) return false
  if (session.user.isPlatformAdmin) return true
  const m = orgMembership(session, orgId)
  if (!m || !m.permissions.includes("members:support")) return false
  if (target.isPlatformAdmin || target.multiOrg) return false
  if (m.role === "owner") return true
  if (m.role === "admin") return target.role !== "owner"
  return target.role === "member"
}

export function canEditTargetMember(session: Session | undefined, orgId: string, target: Member): boolean {
  if (!session) return false
  if (session.user.isPlatformAdmin) return true
  const m = orgMembership(session, orgId)
  if (!m || !m.permissions.includes("members:write")) return false
  if (m.role === "owner") return true
  if (m.role === "admin") return target.role !== "owner"
  return target.role !== "owner" && target.role !== "admin"
}

export function roleBadgeTone(role: Role): "info" | "neutral" | "success" | "warning" {
  switch (role) {
    case "owner":
    case "admin":
      return "info"
    case "domain_admin":
    case "helpdesk":
    case "list_admin":
    case "custom":
      return "warning"
    case "auditor":
    case "member":
      return "neutral"
  }
}

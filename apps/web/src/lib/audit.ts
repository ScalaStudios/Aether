import type { AuditEvent } from "@/lib/types"

const actionLabels: Record<string, string> = {
  "alias.create": "Created alias",
  "alias.delete": "Deleted alias",
  "alias.update": "Updated alias",
  "api_key.create": "Created API key",
  "api_key.revoke": "Revoked API key",
  "app_password.create": "Created app password",
  "app_password.revoke": "Revoked app password",
  "audit.export": "Exported audit log",
  "audit.retention.update": "Changed audit retention",
  "auth.login": "Signed in",
  "auth.login.failed": "Sign-in failed",
  "auth.logout": "Signed out",
  "auth.oidc.rejected": "Single sign-on rejected",
  "branding.image.remove": "Removed branding image",
  "branding.image.update": "Uploaded branding image",
  "branding.update": "Updated branding",
  "calendar.import": "Imported calendar",
  "contacts.import": "Imported contacts",
  "domain.create": "Added domain",
  "domain.delete": "Deleted domain",
  "domain.verify": "Verified domain",
  "identity_provider.create": "Connected identity provider",
  "identity_provider.delete": "Deleted identity provider",
  "identity_provider.event": "Identity provider event",
  "identity_provider.scim_disable": "Turned off SCIM",
  "identity_provider.scim_token": "Issued SCIM token",
  "identity_provider.update": "Updated identity provider",
  "mail_import.start": "Started mail import",
  "mailbox.convert": "Converted mailbox",
  "mailbox.create": "Created mailbox",
  "mailbox.delegate_add": "Added delegate",
  "mailbox.delegate_remove": "Removed delegate",
  "mailbox.delete": "Deleted mailbox",
  "mailbox.messages.copy": "Copied messages",
  "mailbox.messages.move": "Moved messages",
  "mailbox.rules.update": "Updated mail rules",
  "mailbox.signatures.update": "Updated signatures",
  "mailbox.update": "Updated mailbox",
  "member.remove": "Removed member",
  "member.set_role": "Changed member role",
  "organization.create": "Created organization",
  "platform_admin.bootstrap": "Set up platform administrator",
  "scim.user.create": "Provisioned account",
  "scim.user.deprovision": "Deprovisioned account",
  "scim.user.update": "Updated provisioned account",
  "signature_template.remove": "Removed signature template",
  "signature_template.update": "Updated signature template",
  "user.restore": "Restored account",
  "user.suspend": "Suspended account",
  "webhook.create": "Created webhook",
  "webhook.delete": "Deleted webhook",
}

const targetLabels: Record<string, string> = {
  api_key: "API key",
  user: "Account",
}

function humanize(name: string) {
  const words = name.replace(/[._]+/g, " ")
  return words.charAt(0).toUpperCase() + words.slice(1)
}

export const auditActionLabel = (action: string) => actionLabels[action] ?? humanize(action)

export const auditTargetLabel = (type: string) => targetLabels[type] ?? humanize(type)

export const auditCategories = [
  { value: "auth.*", label: "Sign-ins" },
  { value: "mailbox.*,mail_import.*,calendar.*,contacts.*,app_password.*", label: "Mailboxes" },
  { value: "alias.*", label: "Aliases" },
  { value: "domain.*", label: "Domains" },
  { value: "member.*,user.*", label: "Members and accounts" },
  { value: "organization.*,branding.*,signature_template.*", label: "Organization and branding" },
  { value: "api_key.*,webhook.*", label: "API keys and webhooks" },
  { value: "audit.*", label: "Audit log" },
]

export const platformAuditCategory = { value: "identity_provider.*,scim.*,platform_admin.*", label: "Identity and provisioning" }

const text = (value: unknown) => (typeof value === "string" && value ? value : undefined)

export function auditActor(event: AuditEvent): { name: string; note?: string } {
  if (event.actorEmail) return { name: event.actorEmail }
  const key = text(event.metadata.apiKeyName)
  if (key) return { name: key, note: "API key" }
  const attempted = event.action.startsWith("auth.") ? text(event.metadata.email) : undefined
  return attempted ? { name: attempted, note: "unverified" } : { name: "System" }
}

export function auditTargetName(event: AuditEvent) {
  const named = ["address", "name", "email", "url", "slug", "mailbox"].map((key) => text(event.metadata[key])).find(Boolean)
  if (named) return named
  if (event.targetId === event.actorUserId && event.actorEmail) return event.actorEmail
  return event.targetId
}

export function auditDetail(value: unknown) {
  if (value === null || value === undefined || value === "") return "—"
  if (typeof value !== "object") return String(value)
  if (Array.isArray(value) && value.every((item) => typeof item !== "object")) return value.join(", ") || "—"
  return JSON.stringify(value, null, 2)
}

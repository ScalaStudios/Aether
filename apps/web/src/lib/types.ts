export type Role = "owner" | "admin" | "member"

export interface User {
  id: string
  email: string
  displayName: string
  isPlatformAdmin: boolean
  status: "active" | "suspended" | "deprovisioned"
}

export interface Membership {
  orgId: string
  orgName: string
  orgSlug: string
  role: Role
}

export interface Session {
  user: User
  csrfToken: string
  memberships: Membership[]
}

export type ProviderKind = "halo" | "generic"

export interface AuthProviders {
  localLogin: boolean
  providers: { slug: string; name: string; kind: ProviderKind }[]
}

export interface Organization {
  id: string
  name: string
  slug: string
  createdAt: string
}

export interface Domain {
  id: string
  orgId: string
  name: string
  verified: boolean
  verifiedAt?: string | null
  verificationToken: string
  dkimSelector: string
  dkimRecord: string
  active: boolean
  createdAt: string
}

export type DNSStatus = "ok" | "missing" | "mismatch" | "error"

export interface DNSRecord {
  type: "MX" | "TXT" | "CNAME" | "PTR"
  name: string
  value: string
  purpose: "verification" | "mx" | "spf" | "dkim" | "dmarc"
  status: DNSStatus
  found: string[]
}

export interface Mailbox {
  id: string
  orgId: string
  domainId: string
  address: string
  localPart: string
  displayName: string
  ownerUserId?: string | null
  quotaBytes: number
  active: boolean
  createdAt: string
}

export interface Alias {
  id: string
  orgId: string
  domainId: string
  address: string
  localPart: string
  destinations: string[]
  active: boolean
}

export interface Member {
  userId: string
  email: string
  displayName: string
  role: Role
  status: string
}

export interface AuditEvent {
  id: number
  createdAt: string
  actorEmail?: string | null
  action: string
  targetType: string
  targetId: string
  ip?: string
}

export interface AppPassword {
  id: string
  name: string
  mailboxId: string
  mailboxAddress: string
  createdAt: string
}

export type CreatedAppPassword = AppPassword & { password: string }

export type SpecialUse = "" | "inbox" | "drafts" | "sent" | "trash" | "junk" | "archive"

export interface Folder {
  name: string
  specialUse: SpecialUse
  total: number
  unread: number
}

export interface Address {
  name?: string
  address: string
}

export interface MessageSummary {
  uid: number
  subject: string
  from: Address[]
  to: Address[]
  date: string
  seen: boolean
  flagged: boolean
  answered: boolean
  hasAttachments: boolean
  size: number
}

export interface MessagePage {
  messages: MessageSummary[]
  total: number
  nextCursor?: number | null
}

export interface Attachment {
  part: string
  filename: string
  contentType: string
  size: number
}

export interface Message extends MessageSummary {
  cc: Address[]
  replyTo: Address[]
  messageId: string
  inReplyTo: string
  references: string[]
  text: string
  html: string
  attachments: Attachment[]
}

export interface OutgoingMessage {
  to: Address[]
  cc?: Address[]
  bcc?: Address[]
  subject: string
  text?: string
  html?: string
  inReplyTo?: string
  references?: string[]
}

export interface GroupMapping {
  group: string
  orgId: string
  role: Role
}

export interface IdentityProvider {
  id: string
  slug: string
  name: string
  kind: ProviderKind
  issuer: string
  clientId: string
  hasClientSecret: boolean
  hasWebhookSecret: boolean
  webhookUrl: string
  scopes: string[]
  autoProvision: boolean
  adminGroup: string
  groupMappings: GroupMapping[]
  enabled: boolean
  redirectUri: string
}

export interface IdentityProviderInput {
  slug: string
  name: string
  kind: ProviderKind
  issuer: string
  clientId: string
  clientSecret?: string
  webhookSecret?: string
  scopes?: string[]
  autoProvision?: boolean
  adminGroup?: string
  groupMappings?: GroupMapping[]
  enabled?: boolean
}

export interface Discovery {
  issuer: string
  authorizationEndpoint: string
  tokenEndpoint: string
  userinfoEndpoint?: string
  endSessionEndpoint?: string
  scopesSupported: string[]
  pkceS256: boolean
  groupsClaim: boolean
  redirectUri: string
}

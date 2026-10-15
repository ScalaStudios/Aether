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
  threadUids?: number[]
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
  bcc: Address[]
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
  attachments?: { filename: string; contentType: string; data: string }[]
  forwardFrom?: { folder: string; uid: number; parts?: string[] }
  draftUid?: number
}

export interface SavedDraft {
  uid: number
  folder: string
  attachments: Attachment[]
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

export type RuleField = "from" | "to" | "cc" | "subject"

export interface MailRule {
  name: string
  enabled: boolean
  match: "all" | "any"
  conditions: { field: RuleField; contains: string }[]
  actions: { moveTo?: string; markRead?: boolean; flag?: boolean; forwardTo?: string[]; keepCopy?: boolean; stop?: boolean }
}

export interface AutoReply {
  enabled: boolean
  subject: string
  message: string
  start?: string
  end?: string
}

export interface Contact {
  id: string
  name: string
  emails: string[]
  phones: string[]
  organization: string
  notes: string
}

export type ContactInput = Omit<Contact, "id">

export interface Recipient {
  name: string
  address: string
  source: "contact" | "directory"
}

export type Repeat = "" | "daily" | "weekly" | "monthly" | "yearly" | "custom"

export interface CalendarEvent {
  id: string
  title: string
  start: string
  end: string
  seriesStart: string
  allDay: boolean
  location: string
  description: string
  repeat: Repeat
}

export type CalendarEventInput = Omit<CalendarEvent, "id" | "repeat" | "seriesStart"> & { repeat: Exclude<Repeat, "custom"> }

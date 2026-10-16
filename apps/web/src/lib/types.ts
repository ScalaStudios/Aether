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
  purpose: "verification" | "mx" | "spf" | "dkim" | "dmarc" | "mta-sts-host" | "mta-sts" | "tls-rpt" | "autoconfig" | "autodiscover" | "srv-imaps" | "srv-submissions"
  status: DNSStatus
  found: string[]
}

export interface DMARCSource {
  sourceIp: string
  messages: number
  dkimAligned: number
  spfAligned: number
  quarantined: number
  rejected: number
  reporters: string[]
  headerFroms: string[]
}

export interface DMARCSummary {
  days: number
  reports: number
  sources: DMARCSource[]
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
  access?: "owner" | DelegateAccess
}

export type DelegateAccess = "read" | "full"

export interface Delegate {
  userId: string
  email: string
  displayName: string
  access: DelegateAccess
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

export interface APIKey {
  id: string
  name: string
  prefix: string
  scopes: string[]
  createdAt: string
  lastUsedAt: string | null
  expiresAt: string | null
}

export interface Webhook {
  id: string
  url: string
  events: string[]
  active: boolean
  createdAt: string
}

export interface WebhookDelivery {
  id: string
  event: string
  status: "pending" | "delivered" | "failed"
  attempts: number
  responseCode: number | null
  lastError: string | null
  createdAt: string
  deliveredAt: string | null
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
  invitation?: Invitation
  related?: { folder: string; uid: number; date: string }[]
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
  sendAt?: string
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

export interface Signature {
  id: string
  name: string
  html: string
}

export interface SignatureSettings {
  signatures: Signature[]
  newMessage: string
  replies: string
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
  organizer?: string
  attendees: Attendee[]
  calendarId?: string
  readOnly?: boolean
  color?: string
}

export type CalendarAccess = "owner" | "write" | "read"

export interface CalendarShare {
  id: string
  access: Exclude<CalendarAccess, "owner">
  email?: string
  name?: string
  orgId?: string
  orgName?: string
}

export interface CalendarInfo {
  id: string
  name: string
  color: string
  access: CalendarAccess
  owner?: { name: string; email: string }
  shares?: CalendarShare[]
}

export type AttendeeStatus = "needs-action" | "accepted" | "tentative" | "declined"

export interface Attendee {
  name?: string
  address: string
  status: AttendeeStatus
}

export interface Invitation {
  method: "request" | "reply" | "cancel"
  event: CalendarEvent
  organizer: Address
  status: AttendeeStatus | ""
  eventId?: string
  applied: boolean
}

export type CalendarEventInput = Omit<CalendarEvent, "id" | "repeat" | "seriesStart" | "organizer" | "attendees" | "calendarId" | "readOnly" | "color"> & {
  repeat: Exclude<Repeat, "custom">
  attendees: string[]
  calendarId: string
}

export interface ScheduledMessage {
  id: string
  sendAt: string
  subject: string
  to: Address[]
  error: string | null
}

export interface SnoozedMessage {
  uid: number
  until: string
  returnFolder: string
}

export type MailImport = {
  id: string
  host: string
  username: string
  status: "running" | "done" | "failed" | "cancelled" | "interrupted"
  progress: { folder?: string; foldersDone?: number; foldersTotal?: number; copied?: number; skipped?: number }
  error: string
  createdAt: string
  finishedAt: string | null
}

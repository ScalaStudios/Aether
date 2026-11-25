import type { SecurityKeyRequestOptions } from "@/lib/webauthn"

export type Role = "owner" | "admin" | "member"

export interface User {
  id: string
  email: string
  displayName: string
  isPlatformAdmin: boolean
  status: "active" | "suspended" | "deprovisioned"
  title: string
  phone: string
}

export interface Membership {
  orgId: string
  orgName: string
  orgSlug: string
  role: Role
  compliance: boolean
}

export interface Session {
  user: User
  csrfToken: string
  memberships: Membership[]
  twoFactorSetupRequired: boolean
}

export interface TwoFactorPrompt {
  totp: boolean
  securityKey: SecurityKeyRequestOptions | null
}

export type LoginResult = Session | { twoFactor: TwoFactorPrompt }

export interface SecurityKey {
  id: string
  name: string
  createdAt: string
  lastUsedAt: string | null
}

export interface SecurityStatus {
  passwordSignIn: boolean
  twoFactorRequired: boolean
  totp: boolean
  recoveryCodes: number
  securityKeys: SecurityKey[]
}

export interface TOTPSetup {
  secret: string
  uri: string
  qrCode: string
}

export interface ActiveSession {
  id: string
  ip: string
  userAgent: string
  provider: string | null
  createdAt: string
  lastSeenAt: string
  current: boolean
}

export type TwoFactorPolicy = "off" | "admins" | "everyone"

export type ProviderKind = "halo" | "generic"

export interface AuthProviders {
  localLogin: boolean
  providers: { slug: string; name: string; kind: ProviderKind }[]
}

export type Protocol = "imap" | "smtp" | "sieve" | "dav" | "webmail"

export interface Organization {
  id: string
  name: string
  slug: string
  createdAt: string
  spamLearning: boolean
  deletedItemRetentionDays: number
  mailboxProtocols: Protocol[]
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
  coexistenceHost: string
  dualDeliveryMailboxIds: string[]
  dkimRotationDue: boolean
  active: boolean
  aliasOf: string | null
  aliasSendAs: boolean
  createdAt: string
}

export type DNSStatus = "ok" | "warning" | "missing" | "mismatch" | "error"

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

export type MailboxKind = "user" | "shared" | "resource"

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
  kind: MailboxKind
  retainedAt?: string | null
  hiddenFromDirectory: boolean
  protocols: Protocol[]
  access?: "owner" | DelegateAccess
  sendAs?: boolean
  sendOnBehalf?: boolean
  fromAddresses?: Address[]
  usedBytes?: number
  quotaWarningPercents?: number[]
}

export interface QuotaWarnings {
  percents: number[]
  notifyAdmins: boolean
}

export type DelegateAccess = "read" | "full"

export interface Delegate {
  userId: string
  email: string
  displayName: string
  access: DelegateAccess
  sendAs: boolean
  sendOnBehalf: boolean
  createdAt: string
}

export type AcceptFrom = "anyone" | "organization" | "members" | "allowlist"

export interface PersonalAliasPolicy {
  limitPerUser: number
  domains: { id: string; name: string; available: boolean }[]
}

export interface Alias {
  id: string
  orgId: string
  domainId: string
  address: string
  localPart: string
  destinations: string[]
  active: boolean
  kind: "alias" | "list"
  name: string
  acceptFrom: AcceptFrom
  allowedSenders: string[]
  sendAs: string[]
  sentMailboxId?: string
  owners: string[]
  groups: string[]
  memberDomains: string[]
  moderated: boolean
  moderators: string[]
  moderationExempt: string[]
  hiddenFromDirectory: boolean
  personalOwnerId?: string
  note?: string
  expiresAt?: string
}

export interface DirectoryGroup {
  id: string
  name: string
  provider: string
  members: number
}

export interface ResolvedMember {
  address: string
  name: string
  direct: boolean
  domain: boolean
  groups: string[]
}

export interface HeldMessage {
  id: string
  listId: string
  listAddress: string
  listName: string
  sender: string
  from: string
  subject: string
  preview: string
  size: number
  receivedAt: string
  expiresAt: string
}

export interface Member {
  userId: string
  email: string
  displayName: string
  role: Role
  status: string
  compliance: boolean
  passwordSignIn: boolean
  twoFactor: boolean
}

export type ForwardingMode = "allowed" | "domains" | "blocked"

export interface Forwarding {
  mode: ForwardingMode
  domains: string[]
  forwarders: { mailboxId: string; address: string; rule: string; enabled: boolean; to: string; allowed: boolean }[]
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

export type MessageTraceStatus = "rejected" | "bounced" | "discarded" | "deleted" | "deferred" | "held" | "queued" | "unknown" | "junk" | "delivered"

export interface MessageTraceSummary {
  id: number
  receivedAt: string
  status: MessageTraceStatus
  sender: string
  recipients: string[]
  subject: string
  messageId: string
  queueId: string | null
  service: string
  clientIp: string
  tls: string | null
  spamScore: number | null
  spam: boolean
  lastEvent: string | null
  detail: string | null
}

export interface MessageTracePage {
  messages: MessageTraceSummary[]
  retentionDays: number | null
  readAt: string | null
}

export interface MessageTraceEvent {
  at: string
  kind: "delivered" | "relayed" | "forwarded" | "deferred" | "bounced" | "expired" | "rejected" | "discarded" | "held" | "released" | "deleted" | "notified"
  recipient: string
  origRecipient: string
  relay: string
  dsn: string
  detail: string
  tls: string
}

export interface MessageTraceDetail extends MessageTraceSummary {
  clientName: string
  saslUsername: string
  size: number | null
  recipientStatus: Record<string, MessageTraceStatus>
  events: MessageTraceEvent[]
  related: { id: number; receivedAt: string; service: string; sender: string; status: MessageTraceStatus }[]
}

export type QueueName = "maildrop" | "incoming" | "active" | "deferred" | "hold"

export type QueueAction = "retry" | "hold" | "release" | "delete"

export interface QueueFilter {
  sender?: string
  recipientDomain?: string
  error?: string
  queue?: QueueName
}

export interface QueuedMessage {
  id: string
  queue: QueueName
  arrivalTime: string
  size: number
  sender: string
  recipients: { address: string; delayReason?: string }[]
  nextAttempt?: string
}

export interface MailQueue {
  summary: { total: number; queues: Record<QueueName, number>; oldestArrival?: string }
  matched: number
  messages: QueuedMessage[]
}

export interface MailboxAuditSettings {
  enabled: boolean
  logReads: boolean
  logOwnerDeletes: boolean
  ownersCanView: boolean
  retentionDays: number
}

export type AccessRole = "owner" | "delegate" | "member" | "admin"

export interface MailboxAccessEvent {
  id: number
  createdAt: string
  actorEmail: string
  actorRole: AccessRole
  action: string
  folder: string
  subject: string
  messageId: string
  client: "web" | "imap" | "app_password" | "token" | "smtp"
  ip: string
  metadata: Record<string, unknown>
}

export interface MailboxAccessSummary {
  actorEmail: string
  actorRole: AccessRole
  events: number
  first: string
  last: string
}

export interface MailboxAccessReport {
  address: string
  summary: MailboxAccessSummary[]
  events: MailboxAccessEvent[]
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

export interface Suppression {
  id: string
  address: string
  reason: "bounce" | "complaint" | "manual"
  detail: string | null
  createdAt: string
}

export interface AppPassword {
  id: string
  name: string
  mailboxId: string
  mailboxAddress: string
  createdAt: string
  scope: AppPasswordScope
}

export type AppPasswordScope = "all" | "mail" | "dav"

export type CreatedAppPassword = AppPassword & { password: string }

export type SpecialUse = "" | "inbox" | "drafts" | "sent" | "trash" | "junk" | "archive"

export interface Folder {
  name: string
  specialUse: SpecialUse
  total: number
  unread: number
  size: number
  subscribed: boolean
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

export interface RecoverableItem extends MessageSummary {
  folder: string
  deletedAt: string
}

export interface RecoverableList {
  retentionDays: number
  items: RecoverableItem[]
}

export interface MessagePage {
  messages: MessageSummary[]
  total: number
  nextCursor?: string | null
}

export interface Attachment {
  part: string
  filename: string
  contentType: string
  size: number
}

export interface MessageProtection {
  protocol?: "smime" | "openpgp" | ""
  encrypted?: boolean
  signed?: boolean
  autocrypt?: string
}

export interface Message extends MessageSummary {
  deliveredTo?: string
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
  protection?: MessageProtection
  related?: { folder: string; uid: number; date: string }[]
  sender?: SenderInsight | null
}

export type AuthResultValue = "pass" | "fail" | "softfail" | "neutral" | "none" | "temperror" | "permerror"

export interface AuthResult {
  result: AuthResultValue
  domain?: string
}

export interface SenderAuthentication {
  submission: "pass" | "relay" | "none"
  spf: AuthResult
  dkim: AuthResult
  dmarc: AuthResult
}

export interface SenderWarning {
  kind: "spoofed" | "name" | "address-in-name" | "lookalike"
  name?: string
  address?: string
  domain?: string
  resembles?: string
}

export interface SenderInsight {
  organization: string
  external: boolean
  firstContact: boolean
  warnings: SenderWarning[]
  authentication: SenderAuthentication | null
}

export interface SenderWarningSettings {
  externalTag: boolean
  firstContact: boolean
  impersonation: boolean
}

export interface OutgoingMessage {
  fromAddress?: string
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
  sealed?: { contentType: string; body: string }
  autocrypt?: string
}

export type EncryptionLevel = "off" | "allow" | "require"

export interface EncryptionPolicy {
  smime: EncryptionLevel
  openpgp: EncryptionLevel
  password: EncryptionLevel
  domains?: string[]
}

export interface EncryptionKey {
  id: string
  address: string
  protocol: "smime" | "openpgp"
  fingerprint: string
  publicKey: string
  privateKey?: string
  salt?: string
  iterations?: number
  createdAt: string
}

export interface RecipientKey {
  protocol: "smime" | "openpgp"
  fingerprint: string
  publicKey: string
  source: string
}

export interface ProtectedMessage {
  sender: string
  recipient: string
  salt: string
  message: string
  reply?: string
  replied: boolean
  expiresAt: string
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

export interface BlockedAddress {
  id: string
  address: string
  protocol: string
  failures: number
  blocks: number
  blockedAt: string
  blockedUntil: string
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
  scimEnabled: boolean
  scimUrl: string
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

export type RuleField = "from" | "to" | "cc" | "subject" | "body" | "header" | "sizeOver" | "sizeUnder" | "notToMe"

export interface RuleCondition {
  field: RuleField
  contains?: string
  header?: string
  negate?: boolean
  sizeKb?: number
}

export interface MailRule {
  name: string
  enabled: boolean
  match: "all" | "any"
  conditions: RuleCondition[]
  actions: { moveTo?: string; delete?: boolean; markRead?: boolean; flag?: boolean; forwardTo?: string[]; keepCopy?: boolean; stop?: boolean }
}

export type FlowField = "sender" | "recipient" | "subject" | "body" | "header" | "attachmentName" | "attachmentType" | "sizeOver" | "spamScoreOver" | "direction"

export type FlowScope = "address" | "domain" | "inside" | "outside" | "memberOf"

export interface FlowCondition {
  field: FlowField
  scope?: FlowScope
  header?: string
  values?: string[]
  number?: number
}

export interface FlowActions {
  disclaimer?: { text: string; html?: string; fallback: "wrap" | "skip" | "reject" }
  bcc?: string[]
  redirectTo?: string[]
  reject?: string
  addHeaders?: { name: string; value: string }[]
  removeHeaders?: string[]
  spamScore?: number
  requireTls?: boolean
  approvers?: string[]
}

export interface FlowRule {
  id: string
  name: string
  mode: "enforce" | "test" | "off"
  conditions: FlowCondition[]
  exceptions: FlowCondition[]
  actions: FlowActions
  stop: boolean
  updatedAt: string
}

export interface FlowMatch {
  id: number
  ruleId: string | null
  ruleName: string
  test: boolean
  direction: "incoming" | "outgoing" | "internal"
  sender: string
  recipients: string[]
  subject: string
  actions: string[]
  createdAt: string
}

export interface FlowHeldMessage {
  id: string
  orgId: string
  ruleName: string
  approvers: string[]
  sender: string
  recipients: string[]
  subject: string
  preview: string
  createdAt: string
}

export type TaggedMailMode = "inbox" | "create_folder" | "existing_folder" | "reject"

export interface TaggedMail {
  mode: TaggedMailMode
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
  displayName?: string
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

export interface ImportCounts {
  imported: number
  updated: number
  skipped: number
}

export interface Recipient {
  name: string
  address: string
  source: "contact" | "directory"
  kind?: ResourceType
  company?: string
  phone?: string
}

export interface OrgContact {
  id: string
  orgId: string
  name: string
  email: string
  company: string
  phone: string
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
  kind?: ResourceType
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

export type ResourceType = "room" | "equipment"

export interface Resource {
  id: string
  orgId: string
  address: string
  name: string
  type: ResourceType
  capacity: number | null
  location: string
  description: string
  policy: "auto" | "approval"
  bookableBy: "organization" | "selected"
  windowDays: number
  maxMinutes: number | null
  workingHours: boolean
  workDays: number[]
  workStart: number
  workEnd: number
  timeZone: string
}

export interface AdminResource extends Resource {
  domainId: string
  localPart: string
  active: boolean
  approvers: string[]
  bookers: string[]
}

export type ResourceSettings = Omit<Resource, "id" | "orgId" | "address" | "name"> & {
  displayName?: string
  approvers: string[]
  bookers: string[]
}

export interface ResourceChoice extends Resource {
  bookable: boolean
  availability?: "free" | "busy" | "restricted"
  reason?: string
}

export interface BookingOutcome {
  address: string
  name: string
  status: "accepted" | "tentative" | "declined"
  reason?: string
}

export interface BookingRequest {
  id: string
  resource: { id: string; name: string; address: string; type: ResourceType }
  title: string
  start: string
  end: string
  allDay: boolean
  repeat: Repeat
  location: string
  organizer: Address
  conflict?: string
  requestedAt: string
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
  scheduleId: string | null
  host: string
  username: string
  status: "running" | "done" | "failed" | "cancelled" | "interrupted"
  progress: { folder?: string; foldersDone?: number; foldersTotal?: number; copied?: number; skipped?: number; flagsUpdated?: number; deleted?: number }
  error: string
  createdAt: string
  finishedAt: string | null
}

export type MailImportSchedule = {
  id: string
  mailboxId: string
  address: string
  host: string
  username: string
  status: "scheduled" | "stopped" | "expired"
  intervalMinutes: number
  endsAt: string | null
  nextRunAt: string | null
  latestRun: MailImport | null
}

export type BrandTheme = "dark" | "light"

export type BrandColorKey = "accent" | "accentHover" | "accentActive" | "background" | "surface" | "border" | "text" | "link"

export type BrandColors = Record<BrandTheme, Partial<Record<BrandColorKey, string>>>

export interface BrandImages {
  logoLight: string | null
  logoDark: string | null
  favicon: string | null
}

export interface Branding extends BrandImages {
  orgId: string
  name: string
  signInMessage: string
  colors: BrandColors
  customCssUrl: string | null
}

export interface BrandingSettings {
  name: string
  signInMessage: string
  colors: BrandColors
  customCss: string
}

export type OrgBranding = BrandingSettings & BrandImages & { customCssEnabled: boolean }

export type AttachmentAction = "off" | "reject" | "remove"

export interface AttachmentRules {
  action: AttachmentAction
  extensions: string[]
}

export interface AttachmentPolicy {
  inbound: AttachmentRules
  outbound: AttachmentRules
  exemptSenders: string[]
}

export type OrgAttachmentPolicy = AttachmentPolicy & { defaultExtensions: string[] }

export interface SignatureLink {
  label: string
  url: string
}

export interface SignatureTemplate {
  domainId: string | null
  html: string
  links: SignatureLink[]
  logoWidth: number
  updatedAt?: string
}

export type QuarantineCategory = "spam" | "high_confidence_spam" | "phishing" | "malware"

export type QuarantineAccess = "release" | "request" | "none"

export type QuarantineStatus = "held" | "requested" | "released"

export interface QuarantineItem {
  id: string
  mailboxId: string
  mailbox: string
  recipient: string
  category: QuarantineCategory
  access: QuarantineAccess
  status: QuarantineStatus
  from: Address
  subject: string
  score: number
  size: number
  receivedAt: string
  expiresAt: string
  requestedAt?: string | null
  releasedAt?: string | null
  releasedBy?: string | null
}

export interface QuarantineSymbol {
  name: string
  score: number
  options?: string[]
}

export interface QuarantinePreview extends QuarantineItem {
  message: Message
  symbols: QuarantineSymbol[]
  canAllowSender: boolean
}

export interface QuarantinePage {
  items: QuarantineItem[]
  nextCursor: string | null
  total?: number
  retentionDays?: number
  requested?: number
}

export type QuarantineDigest = "off" | "daily" | "weekly"

export interface QuarantinePolicy {
  access: Record<QuarantineCategory, QuarantineAccess>
  retentionDays: number
  digest: QuarantineDigest
}

export interface AllowedSender {
  sender: string
  createdAt: string
}

export type SenderAction = "allow" | "block"

export interface SenderEntry {
  id: string
  domainId: string | null
  action: SenderAction
  kind: "address" | "domain" | "ip"
  value: string
  note: string
  expiresAt: string | null
  createdAt: string
}

export type RetentionAction = "delete" | "keep"

export interface RetentionPolicy {
  id: string
  mailboxId: string | null
  mailboxAddress: string | null
  action: RetentionAction
  folder: string
  days: number | null
  updatedAt: string
}

export interface RetentionRule {
  folder: string
  days: number
}

export interface MailboxRetention {
  keepDays: number
  folders: RetentionRule[]
}

export type CaseScope = "mailboxes" | "domains" | "organization"

export interface CaseCriteria {
  scope: CaseScope
  mailboxIds: string[]
  domainIds: string[]
  from: string
  to: string
  subject: string
  body: string
  attachment: string
  since: string
  until: string
}

export interface ComplianceCase {
  id: string
  orgId: string
  name: string
  description: string
  criteria: CaseCriteria
  createdBy: string | null
  createdAt: string
  updatedAt: string
  closedAt: string | null
  holds: number
}

export interface ComplianceMailbox {
  id: string
  domainId: string
  address: string
  displayName: string
  kind: MailboxKind
  active: boolean
  retainedAt: string | null
  held: boolean
}

export interface ComplianceDirectory {
  mailboxes: ComplianceMailbox[]
  domains: { id: string; name: string }[]
}

export interface Hold {
  mailboxId: string
  criteria: CaseCriteria | null
  address: string
  displayName: string
  active: boolean
  retainedAt: string | null
  createdBy: string | null
  createdAt: string
}

export interface SearchHit {
  mailboxId: string
  mailbox: string
  folder: string
  uid: number
  messageId: string
  subject: string
  from: Address[]
  to: Address[]
  date: string
  size: number
  attachments: string[]
}

export interface CaseSearchResult {
  results: SearchHit[]
  truncated: boolean
  searched: number
  skipped: string[]
  failed: { mailbox: string; error: string }[]
}

export interface JournalRule {
  id: string
  match: string
  archiveAddress: string
  createdBy: string | null
  createdAt: string
}

export type SendingLimitKey = "messagesPerHour" | "messagesPerDay" | "recipientsPerMessage" | "recipientsPerDay" | "newRecipientsPerDay" | "spamPercent"

export type SendingLimits = Partial<Record<SendingLimitKey, number>>

export interface SendingLimitSettings {
  defaults: Record<SendingLimitKey, number>
  organization: SendingLimits
  domains: Record<string, SendingLimits>
  mailboxes: Record<string, SendingLimits>
}

export interface RestrictedSender {
  mailboxId: string
  address: string
  displayName: string
  restrictedAt: string
  reason: SendingLimitKey
}

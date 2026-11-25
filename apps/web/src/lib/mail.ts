import { Archive, File, Folder as FolderIcon, Inbox, OctagonAlert, Send, Trash2, type LucideIcon } from "lucide-react"

import { formatAddress, formatDateTime, plural } from "@/lib/format"
import type { Address, Attachment, Folder, Label, LabelColor, Mailbox, MailboxRetention, Message, SpecialUse } from "@/lib/types"

export const folderIcons: Record<SpecialUse, LucideIcon> = {
  inbox: Inbox,
  drafts: File,
  sent: Send,
  archive: Archive,
  junk: OctagonAlert,
  trash: Trash2,
  "": FolderIcon,
}

export const snoozedFolder = "Snoozed"

export function openUnsubscribeLink(url: string) {
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return false
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") return false
  window.open(parsed.href, "_blank", "noopener,noreferrer")
  return true
}

export function folderLabel(folder: Pick<Folder, "name" | "specialUse">) {
  return folder.specialUse === "inbox" ? "Inbox" : folder.name
}

export function retentionNotice(retention: MailboxRetention | undefined, folder: Pick<Folder, "name" | "specialUse">) {
  const rule = retention?.folders.find((r) => r.folder === folder.name)
  if (!rule || !retention) return undefined
  const label = folderLabel(folder)
  if (folder.name === "Trash" || folder.name === "Junk") {
    const keep = retention.keepDays ? `, once they are at least ${plural(retention.keepDays, "day")} old` : ""
    return `Messages in ${label} are deleted after ${plural(rule.days, "day")}${keep}.`
  }
  return `Messages in ${label} are deleted ${plural(Math.max(rule.days, retention.keepDays), "day")} after they arrive.`
}

export function folderParent(name: string) {
  return name.includes("/") ? name.slice(0, name.lastIndexOf("/")) : ""
}

export function folderTree(folders: Folder[]) {
  const names = new Set(folders.map((f) => f.name))
  const parent = (f: Folder) => (names.has(folderParent(f.name)) ? folderParent(f.name) : "")
  const out: { folder: Folder; depth: number; label: string }[] = []
  const visit = (name: string, depth: number) => {
    for (const f of folders.filter((f) => parent(f) === name)) {
      out.push({ folder: f, depth, label: name ? f.name.slice(name.length + 1) : folderLabel(f) })
      visit(f.name, depth + 1)
    }
  }
  visit("", 0)
  return out
}

export type StorageTier = "ok" | "warning" | "critical" | "full"

export function storageTier(used: number, quota: number, levels: number[] = []): StorageTier {
  const percent = quota ? (used / quota) * 100 : 0
  if (percent >= 100) return "full"
  if (levels.length > 1 && percent >= Math.max(...levels)) return "critical"
  if (percent >= Math.min(...levels)) return "warning"
  return "ok"
}

export type MessageSort = "date" | "from" | "to" | "subject" | "size"
export type QuickFilter = "unread" | "flagged" | "hasAttachments"

export interface ListView {
  sort: MessageSort
  order: "asc" | "desc"
  filters: QuickFilter[]
  labelId?: string
}

export const labelPalette: Record<LabelColor, { name: string; hex: string }> = {
  red: { name: "Red", hex: "#e5484d" },
  orange: { name: "Orange", hex: "#f76b15" },
  yellow: { name: "Yellow", hex: "#ffc53d" },
  green: { name: "Green", hex: "#30a46c" },
  blue: { name: "Blue", hex: "#3b82f6" },
  purple: { name: "Purple", hex: "#8e4ec6" },
  gray: { name: "Gray", hex: "#8b8d98" },
  pink: { name: "Pink", hex: "#d6409f" },
}

export const labelColorOrder = ["red", "orange", "yellow", "green", "blue", "purple", "gray", "pink"] as const satisfies readonly LabelColor[]

const builtinKeywords: Record<string, { name: string; color: LabelColor }> = {
  $label1: { name: "Important", color: "red" },
  $label2: { name: "Work", color: "orange" },
  $label3: { name: "Personal", color: "green" },
  $label4: { name: "To Do", color: "blue" },
  $label5: { name: "Later", color: "purple" },
}

export interface AppliedLabel {
  keyword: string
  name: string
  color: LabelColor
  id?: string
}

export function appliedLabels(keywords: string[] | undefined, labels: Pick<Label, "id" | "keyword" | "name" | "color">[]): AppliedLabel[] {
  return (keywords ?? []).map((keyword) => {
    const label = labels.find((item) => item.keyword === keyword)
    if (label) return { keyword, name: label.name, color: label.color, id: label.id }
    const builtin = builtinKeywords[keyword]
    if (builtin) return { keyword, name: builtin.name, color: builtin.color }
    return { keyword, name: keyword, color: "gray" }
  })
}

export function showFlagColor(flagColor: string | undefined, applied: AppliedLabel[]) {
  return !!flagColor && flagColor in labelPalette && !applied.some((label) => label.id && label.color === flagColor)
}

export function filtersFromFlags(flags: { unread?: boolean; flagged?: boolean; hasAttachments?: boolean }): QuickFilter[] {
  return [flags.unread ? "unread" : "", flags.flagged ? "flagged" : "", flags.hasAttachments ? "hasAttachments" : ""].filter((item): item is QuickFilter => item !== "")
}

export function savedSearchBody(search: {
  name: string
  query: string
  unread: boolean
  flagged: boolean
  hasAttachments: boolean
  from?: string
  to?: string
  since?: string
  until?: string
  labelId?: string
  folder: string
  allFolders: boolean
}) {
  return {
    name: search.name,
    query: search.query,
    unread: search.unread,
    flagged: search.flagged,
    hasAttachments: search.hasAttachments,
    from: search.from ?? "",
    to: search.to ?? "",
    since: search.since ?? "",
    until: search.until ?? "",
    labelId: search.labelId ?? "",
    folder: search.allFolders ? "" : search.folder,
    allFolders: search.allFolders,
  }
}

let messageDrag: { uid: number; folder: string } | null = null

export function beginMessageDrag(uid: number, folder: string) {
  messageDrag = { uid, folder }
}

export function endMessageDrag() {
  messageDrag = null
}

export function messageDragFolder() {
  return messageDrag?.folder ?? ""
}

export function describeSearch(input: { query: string; filters: QuickFilter[]; label?: string; folder: string; allFolders: boolean }) {
  const parts = input.filters.map((filter) => (filter === "hasAttachments" ? "with attachments" : filter))
  let text = parts.length ? `${parts.join(", ")} messages` : "Messages"
  if (input.label) text += ` labeled ${input.label}`
  if (input.query) text += ` matching “${input.query}”`
  text += input.allFolders ? " in every folder" : ` in ${input.folder === "INBOX" ? "Inbox" : input.folder}`
  return `${text[0].toUpperCase()}${text.slice(1)}.`
}

export const defaultListView: ListView = { sort: "date", order: "desc", filters: [] }

export const dragTypes = { message: "application/x-aether-message", messageFolder: "application/x-aether-message-folder", folder: "application/x-aether-folder" }

export function senderLabel(addresses: Address[], empty = "Unknown sender") {
  const first = addresses[0]
  if (!first) return empty
  return first.name || first.address
}

export type ComposeMode = "new" | "reply" | "replyAll" | "forward" | "draft"

export interface SenderIdentity {
  mailbox: Mailbox
  from: Address
}

export function senderIdentities(mailboxes: Mailbox[], preferredId?: string) {
  const identities = new Map<string, SenderIdentity>()
  for (const mailbox of mailboxes) {
    for (const from of mailbox.fromAddresses ?? []) {
      if (!identities.has(from.address) || mailbox.id === preferredId) identities.set(from.address, { mailbox, from })
    }
  }
  return [...identities.values()]
}

export function composeIdentity(mode: ComposeMode, source: Message | undefined, identities: SenderIdentity[], fallback: SenderIdentity) {
  if (!source || (mode !== "draft" && mode !== "reply" && mode !== "replyAll")) return fallback
  const addresses = mode === "draft" ? source.from : [{ address: source.deliveredTo ?? "" }, ...source.to, ...source.cc, ...source.bcc]
  for (const address of addresses) {
    const match = identities.find((i) => i.from.address === address.address.toLowerCase())
    if (match) return match
  }
  return fallback
}

export interface Draft {
  mode: ComposeMode
  to: Address[]
  cc: Address[]
  bcc: Address[]
  subject: string
  html: string
  inReplyTo?: string
  references?: string[]
  forwardFrom?: { folder: string; uid: number; attachments: Attachment[] }
  draftUid?: number
}

function escapeHtml(text: string) {
  return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;")
}

function bodyText(message: Message) {
  if (message.text) return message.text
  return new DOMParser().parseFromString(message.html, "text/html").body.textContent ?? ""
}

function textToHtml(text: string) {
  return text
    .trim()
    .split(/\n{2,}/)
    .map((block) => `<p>${escapeHtml(block).replace(/\n/g, "<br>")}</p>`)
    .join("")
}

function prefixed(prefix: string, subject: string) {
  return new RegExp(`^${prefix}:`, "i").test(subject.trim()) ? subject : `${prefix}: ${subject}`
}

function withoutSelf(addresses: Address[], self: string[]) {
  const seen = new Set(self.map((address) => address.toLowerCase()))
  return addresses.filter((a) => {
    const key = a.address.toLowerCase()
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

export function buildDraft(mode: ComposeMode, source: Message | undefined, self: string[], folder: string): Draft {
  const empty: Draft = { mode, to: [], cc: [], bcc: [], subject: "", html: "" }
  if (mode === "new" || !source) return { ...empty, mode: "new" }

  if (mode === "draft") {
    return {
      mode,
      to: source.to,
      cc: source.cc,
      bcc: source.bcc,
      subject: source.subject,
      html: source.html || textToHtml(source.text),
      inReplyTo: source.inReplyTo || undefined,
      references: source.references.length ? source.references : undefined,
      draftUid: source.uid,
      forwardFrom: { folder, uid: source.uid, attachments: source.attachments },
    }
  }

  const from = source.from.map(formatAddress).join(", ")

  if (mode === "forward") {
    const header = [
      "---------- Forwarded message ----------",
      `From: ${from}`,
      `Date: ${formatDateTime(source.date)}`,
      `Subject: ${source.subject}`,
      `To: ${source.to.map(formatAddress).join(", ")}`,
    ]
    return {
      ...empty,
      subject: prefixed("Fwd", source.subject),
      forwardFrom: source.attachments.length ? { folder, uid: source.uid, attachments: source.attachments } : undefined,
      html: `<p></p><p>${header.map(escapeHtml).join("<br>")}</p>${textToHtml(bodyText(source))}`,
    }
  }

  const replyTo = source.replyTo.length ? source.replyTo : source.from
  const to = mode === "replyAll" ? withoutSelf([...replyTo, ...source.to], self) : replyTo
  const toKeys = new Set(to.map((a) => a.address.toLowerCase()))
  const cc = mode === "replyAll" ? withoutSelf(source.cc, self).filter((a) => !toKeys.has(a.address.toLowerCase())) : []

  return {
    mode,
    to,
    cc,
    bcc: [],
    subject: prefixed("Re", source.subject),
    html: `<p></p><p>On ${escapeHtml(formatDateTime(source.date))}, ${escapeHtml(from)} wrote:</p><blockquote>${textToHtml(bodyText(source))}</blockquote>`,
    inReplyTo: source.messageId || undefined,
    references: [...source.references, source.messageId].filter(Boolean),
  }
}

export function messageDocument(html: string, loadImages = false) {
  const origin = loadImages && typeof window !== "undefined" ? ` ${window.location.origin}` : ""
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data: cid:${origin}; style-src 'unsafe-inline'"><style>html{color-scheme:light}body{margin:0;padding:24px;font:15px/1.6 -apple-system,"Segoe UI",Roboto,sans-serif;color:#2a2724;background:#fff;overflow-wrap:anywhere}img{max-width:100%;height:auto}</style></head><body>${html}</body></html>`
}

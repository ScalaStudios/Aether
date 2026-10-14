import { Archive, File, Folder as FolderIcon, Inbox, OctagonAlert, Send, Trash2, type LucideIcon } from "lucide-react"

import { formatAddress, formatDateTime } from "@/lib/format"
import type { Address, Attachment, Folder, Message, SpecialUse } from "@/lib/types"

export const folderIcons: Record<SpecialUse, LucideIcon> = {
  inbox: Inbox,
  drafts: File,
  sent: Send,
  archive: Archive,
  junk: OctagonAlert,
  trash: Trash2,
  "": FolderIcon,
}

export function folderLabel(folder: Pick<Folder, "name" | "specialUse">) {
  return folder.specialUse === "inbox" ? "Inbox" : folder.name
}

export function senderLabel(addresses: Address[], empty = "Unknown sender") {
  const first = addresses[0]
  if (!first) return empty
  return first.name || first.address
}

export type ComposeMode = "new" | "reply" | "replyAll" | "forward" | "draft"

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

function withoutSelf(addresses: Address[], self: string) {
  const seen = new Set([self.toLowerCase()])
  return addresses.filter((a) => {
    const key = a.address.toLowerCase()
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

export function buildDraft(mode: ComposeMode, source: Message | undefined, self: string, folder: string): Draft {
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

export function messageDocument(html: string) {
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data: cid:; style-src 'unsafe-inline'"><style>html{color-scheme:light}body{margin:0;padding:24px;font:15px/1.6 -apple-system,"Segoe UI",Roboto,sans-serif;color:#2a2724;background:#fff;overflow-wrap:anywhere}img{max-width:100%;height:auto}</style></head><body>${html}</body></html>`
}

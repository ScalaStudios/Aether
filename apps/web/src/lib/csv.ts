import type { AcceptFrom, Alias, BulkOperation, BulkOperationType, Mailbox, MailboxKind, Member } from "@/lib/types"

const MB = 1024 * 1024
export const MAX_CSV_ROWS = 1000
export const MAX_CSV_BYTES = 4 * 1024 * 1024

export const csvHeaders: Record<BulkOperationType, string[]> = {
  mailbox: ["address", "display_name", "kind", "quota_mb", "password", "active"],
  alias: ["address", "destinations", "active"],
  list: ["address", "name", "members", "accept_from", "allowed_senders", "send_as", "owners", "active"],
}

const acceptFromSet = new Set<string>(["anyone", "organization", "members", "allowlist"])

interface RawCsvLine {
  rowNumber: number
  cells: string[]
}

export interface ParsedCsvRow {
  rowNumber: number
  raw: Record<string, string>
  operation: BulkOperation
  parseError?: string
}

export function parseCsvLines(input: string): { lines: RawCsvLine[]; error?: string } {
  const text = input.charCodeAt(0) === 0xfeff ? input.slice(1) : input
  const lines: RawCsvLine[] = []
  let cells: string[] = []
  let cell = ""
  let inQuotes = false
  let lineNum = 1
  let rowStart = 1

  const finishRow = () => {
    cells.push(cell)
    cell = ""
    if (cells.some((c) => c.trim() !== "")) {
      lines.push({ rowNumber: rowStart, cells })
    }
    cells = []
    rowStart = lineNum
  }

  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (inQuotes) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          cell += '"'
          i++
        } else {
          inQuotes = false
        }
      } else {
        if (ch === "\r") {
          if (text[i + 1] === "\n") i++
          lineNum++
          cell += "\n"
        } else {
          if (ch === "\n") lineNum++
          cell += ch
        }
      }
      continue
    }

    if (ch === '"') {
      if (cell.length === 0) {
        inQuotes = true
      } else {
        cell += ch
      }
    } else if (ch === ",") {
      cells.push(cell)
      cell = ""
    } else if (ch === "\r") {
      if (text[i + 1] === "\n") i++
      lineNum++
      finishRow()
    } else if (ch === "\n") {
      lineNum++
      finishRow()
    } else {
      cell += ch
    }
  }

  if (inQuotes) {
    return { lines: [], error: "The CSV file contains an unclosed quote." }
  }
  if (cell.length > 0 || cells.length > 0) {
    finishRow()
  }
  return { lines }
}

function escapeCsvCell(value: string): string {
  if (/[",\r\n]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`
  }
  return value
}

export function toCsv(headers: string[], rows: string[][]): string {
  const all = [headers, ...rows]
  return all.map((row) => row.map(escapeCsvCell).join(",")).join("\r\n") + "\r\n"
}

export function downloadCsv(filename: string, content: string) {
  const blob = new Blob([content], { type: "text/csv;charset=utf-8" })
  const url = URL.createObjectURL(blob)
  const link = document.createElement("a")
  link.href = url
  link.download = filename
  link.click()
  URL.revokeObjectURL(url)
}

function formatQuotaMb(quotaBytes: number): string {
  if (!quotaBytes) return ""
  const mb = quotaBytes / MB
  return Number.isInteger(mb) ? String(mb) : String(Number(mb.toFixed(2)))
}

export function exportMailboxesCsv(mailboxes: Mailbox[]): string {
  return toCsv(
    csvHeaders.mailbox,
    mailboxes.map((m) => [m.address, m.displayName, m.kind, formatQuotaMb(m.quotaBytes), "", String(m.active)])
  )
}

export function exportAliasesCsv(aliases: Alias[]): string {
  return toCsv(
    csvHeaders.alias,
    aliases.map((a) => [a.address, a.destinations.join(";"), String(a.active)])
  )
}

export function exportListsCsv(lists: Alias[], members: Member[]): string {
  const emailById = new Map(members.map((m) => [m.userId, m.email]))
  return toCsv(
    csvHeaders.list,
    lists.map((l) => [
      l.address,
      l.name,
      l.destinations.join(";"),
      l.acceptFrom,
      l.allowedSenders.join(";"),
      l.sendAs.join(";"),
      l.owners.map((id) => emailById.get(id) ?? id).join(";"),
      String(l.active),
    ])
  )
}

export function templateCsv(kind: BulkOperationType, domain = "example.com"): string {
  const d = domain || "example.com"
  if (kind === "mailbox") {
    return toCsv(csvHeaders.mailbox, [
      [`alice@${d}`, "Alice Smith", "user", "2048", "", "true"],
      [`bob@${d}`, "Bob Jones", "user", "1024", "change-this-password", "true"],
      [`support@${d}`, "Support", "shared", "5120", "", "true"],
    ])
  }
  if (kind === "alias") {
    return toCsv(csvHeaders.alias, [
      [`sales@${d}`, `alice@${d};bob@${d}`, "true"],
      [`@${d}`, `alice@${d}`, "true"],
    ])
  }
  return toCsv(csvHeaders.list, [
    [`staff@${d}`, "Staff", `alice@${d};bob@${d}`, "organization", "", `alice@${d}`, `alice@${d}`, "true"],
    [`news@${d}`, "Announcements", `alice@${d};bob@${d}`, "allowlist", `@${d}`, `alice@${d}`, `alice@${d}`, "true"],
  ])
}

function normalizeHeader(name: string): string {
  return name.trim().toLowerCase().replace(/[\s-]+/g, "_")
}

function headerKey(kind: BulkOperationType, raw: string): string | null {
  const h = normalizeHeader(raw)
  if (h === "address" || h === "email") return "address"
  if (h === "active") return "active"
  if (kind === "mailbox") {
    if (h === "display_name" || h === "displayname" || h === "name") return "display_name"
    if (h === "kind" || h === "type") return "kind"
    if (h === "quota_mb" || h === "quotamb" || h === "quota") return "quota_mb"
    if (h === "quota_bytes" || h === "quotabytes") return "quota_bytes"
    if (h === "password") return "password"
  }
  if (kind === "alias") {
    if (h === "destinations" || h === "destination" || h === "forward_to" || h === "forwardto") return "destinations"
  }
  if (kind === "list") {
    if (h === "name" || h === "display_name" || h === "displayname") return "name"
    if (h === "members" || h === "member" || h === "destinations" || h === "destination") return "members"
    if (h === "accept_from" || h === "acceptfrom" || h === "who_can_send") return "accept_from"
    if (h === "allowed_senders" || h === "allowedsenders") return "allowed_senders"
    if (h === "send_as" || h === "sendas") return "send_as"
    if (h === "owners" || h === "owner") return "owners"
  }
  return null
}

function parseBool(value: string): { value?: boolean; error?: string } {
  const v = value.trim().toLowerCase()
  if (v === "") return {}
  if (v === "true" || v === "yes" || v === "1") return { value: true }
  if (v === "false" || v === "no" || v === "0") return { value: false }
  return { error: "active must be true or false." }
}

function parseAddressList(value: string): string[] {
  return value
    .split(/[;,\s]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

function parseQuota(quotaMb: string, quotaBytes: string): { bytes: number; error?: string } {
  const mb = quotaMb.trim()
  if (mb !== "") {
    if (!/^\d+(\.\d+)?$/.test(mb)) {
      return { bytes: 0, error: "quota_mb must be a non-negative number of megabytes." }
    }
    const parsed = Number(mb)
    if (!Number.isFinite(parsed) || parsed < 0 || parsed * MB > Number.MAX_SAFE_INTEGER) {
      return { bytes: 0, error: "quota_mb is out of range." }
    }
    return { bytes: Math.round(parsed * MB) }
  }
  const bytes = quotaBytes.trim()
  if (bytes !== "") {
    if (!/^\d+$/.test(bytes)) {
      return { bytes: 0, error: "quota_bytes must be a non-negative whole number." }
    }
    const parsed = Number(bytes)
    if (!Number.isSafeInteger(parsed)) {
      return { bytes: 0, error: "quota_bytes is out of range." }
    }
    return { bytes: parsed }
  }
  return { bytes: 0 }
}

export function parseImportCsv(kind: BulkOperationType, text: string): { rows: ParsedCsvRow[]; fileError?: string } {
  const { lines, error } = parseCsvLines(text)
  if (error) return { rows: [], fileError: error }
  if (lines.length === 0) return { rows: [], fileError: "The CSV file is empty." }

  const headerRow = lines[0].cells
  const mapped = headerRow.map((h) => headerKey(kind, h))
  if (!mapped.includes("address")) {
    return { rows: [], fileError: "Missing required column: address." }
  }
  if (kind === "alias" && !mapped.includes("destinations")) {
    return { rows: [], fileError: "Missing required column: destinations." }
  }
  if (kind === "list" && !mapped.includes("members")) {
    return { rows: [], fileError: "Missing required column: members." }
  }

  const dataLines = lines.slice(1)
  if (dataLines.length === 0) {
    return { rows: [], fileError: "The CSV file has headers but no data rows." }
  }
  if (dataLines.length > MAX_CSV_ROWS) {
    return { rows: [], fileError: `Import at most ${MAX_CSV_ROWS.toLocaleString()} rows at a time (found ${dataLines.length.toLocaleString()}).` }
  }

  const rows: ParsedCsvRow[] = dataLines.map(({ rowNumber, cells }) => {
    const raw: Record<string, string> = {}
    for (let i = 0; i < mapped.length; i++) {
      const key = mapped[i]
      if (key && raw[key] === undefined) {
        raw[key] = cells[i] ?? ""
      }
    }
    const address = (raw.address ?? "").trim()
    const activeParsed = parseBool(raw.active ?? "")
    let parseError = activeParsed.error

    if (kind === "mailbox") {
      const rawKind = (raw.kind ?? "").trim().toLowerCase()
      let mbKind: MailboxKind = "user"
      if (rawKind !== "") {
        if (rawKind === "user" || rawKind === "shared") {
          mbKind = rawKind
        } else {
          parseError = parseError ?? "kind must be user or shared."
        }
      }
      const quota = parseQuota(raw.quota_mb ?? "", raw.quota_bytes ?? "")
      if (quota.error) parseError = parseError ?? quota.error
      const operation: BulkOperation = {
        type: "mailbox",
        address,
        displayName: (raw.display_name ?? "").trim(),
        kind: mbKind,
        quotaBytes: quota.bytes,
        password: raw.password ?? "",
        active: activeParsed.value,
      }
      return { rowNumber, raw, operation, parseError }
    }

    if (kind === "alias") {
      const operation: BulkOperation = {
        type: "alias",
        address,
        destinations: parseAddressList(raw.destinations ?? ""),
        active: activeParsed.value,
      }
      return { rowNumber, raw, operation, parseError }
    }

    const rawAccept = (raw.accept_from ?? "").trim().toLowerCase()
    let acceptFrom: AcceptFrom | undefined
    if (rawAccept !== "") {
      if (acceptFromSet.has(rawAccept)) {
        acceptFrom = rawAccept as AcceptFrom
      } else {
        parseError = parseError ?? "accept_from must be anyone, organization, members, or allowlist."
      }
    }
    const operation: BulkOperation = {
      type: "list",
      address,
      name: (raw.name ?? "").trim(),
      destinations: parseAddressList(raw.members ?? ""),
      acceptFrom,
      allowedSenders: parseAddressList(raw.allowed_senders ?? ""),
      sendAs: parseAddressList(raw.send_as ?? ""),
      owners: parseAddressList(raw.owners ?? ""),
      active: activeParsed.value,
    }
    return { rowNumber, raw, operation, parseError }
  })

  return { rows }
}

export function failedRowsCsv(kind: BulkOperationType, rows: { raw: Record<string, string>; error: string }[]): string {
  const headers = [...csvHeaders[kind], "error"]
  return toCsv(
    headers,
    rows.map(({ raw, error }) => [...csvHeaders[kind].map((h) => raw[h] ?? ""), error])
  )
}

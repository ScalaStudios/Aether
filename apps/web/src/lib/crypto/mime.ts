import { base64ToBytes, bytesToBase64, concat, randomHex, text, utf8 } from "@/lib/crypto/bytes"

export type MimeEntity = { contentType: string; body: Uint8Array }

export type ParsedMail = {
  headers: Map<string, string>
  text: string
  html: string
  attachments: { filename: string; contentType: string; data: Uint8Array }[]
}

function crlf(value: string) {
  return value.replace(/\r\n/g, "\n").replace(/\n/g, "\r\n")
}

function isText(contentType: string) {
  const type = contentType.split(";")[0].trim().toLowerCase()
  return type.startsWith("text/") || type.startsWith("multipart/") || type === "application/pgp-encrypted"
}

function encodeBody(contentType: string, body: Uint8Array) {
  if (isText(contentType)) return body
  return utf8(bytesToBase64(body).replace(/(.{76})/g, "$1\r\n") + "\r\n")
}

function part(contentType: string, body: Uint8Array) {
  const encoding = isText(contentType) ? "8bit" : "base64"
  return concat([utf8(`Content-Type: ${contentType}\r\nContent-Transfer-Encoding: ${encoding}\r\n\r\n`), encodeBody(contentType, body)])
}

export function buildEntity(input: { text: string; html: string; attachments?: { filename: string; contentType: string; data: Uint8Array }[] }): MimeEntity {
  const textBody = utf8(crlf(input.text))
  const pieces: { contentType: string; body: Uint8Array }[] = [{ contentType: "text/plain; charset=utf-8", body: textBody }]
  if (input.html.trim()) pieces.push({ contentType: "text/html; charset=utf-8", body: utf8(crlf(input.html)) })
  let entity = pieces.length === 1 ? { contentType: pieces[0].contentType, body: pieces[0].body } : mixed("multipart/alternative", pieces)
  const files = input.attachments ?? []
  if (files.length) {
    entity = mixed("multipart/mixed", [{ contentType: entity.contentType, body: entity.body, raw: true }, ...files.map((file) => ({ contentType: file.contentType || "application/octet-stream", body: file.data, filename: file.filename }))])
  }
  return entity
}

function mixed(subtype: string, parts: { contentType: string; body: Uint8Array; raw?: boolean; filename?: string }[]) {
  const boundary = `aether_${randomHex(12)}`
  const chunks = parts.flatMap((item) => {
    const rendered = item.filename ? filePart(item) : part(item.contentType, item.body)
    return [utf8(`--${boundary}\r\n`), rendered, utf8("\r\n")]
  })
  chunks.push(utf8(`--${boundary}--\r\n`))
  return { contentType: `${subtype}; boundary="${boundary}"`, body: concat(chunks) }
}

function filePart(item: { contentType: string; body: Uint8Array; filename?: string }) {
  const name = (item.filename || "attachment").replace(/[\r\n"]/g, "")
  const encoded = bytesToBase64(item.body).replace(/(.{76})/g, "$1\r\n")
  return concat([
    utf8(`Content-Type: ${item.contentType || "application/octet-stream"}; name="${name}"\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename="${name}"\r\n\r\n`),
    utf8(encoded),
  ])
}

export function entityBytes(entity: MimeEntity) {
  return concat([utf8(`Content-Type: ${entity.contentType}\r\n\r\n`), entity.body])
}

export function pgpEncrypted(armored: string): MimeEntity {
  const boundary = `aether_${randomHex(12)}`
  const body = [
    `--${boundary}`,
    "Content-Type: application/pgp-encrypted",
    "",
    "Version: 1",
    `--${boundary}`,
    "Content-Type: application/octet-stream",
    "",
    armored.trim(),
    `--${boundary}--`,
    "",
  ].join("\r\n")
  return { contentType: `multipart/encrypted; protocol="application/pgp-encrypted"; boundary="${boundary}"`, body: utf8(body) }
}

export function multipartSigned(protocol: string, micalg: string, signed: Uint8Array, signature: Uint8Array, signatureType: string, encoding: "base64" | "7bit" = "base64"): MimeEntity {
  const boundary = `aether_${randomHex(12)}`
  const encoded = encoding === "7bit" ? text(signature).replace(/\r?\n/g, "\r\n").trimEnd() : bytesToBase64(signature).replace(/(.{76})/g, "$1\r\n")
  const body = concat([
    utf8(`--${boundary}\r\n`),
    signed,
    utf8(`\r\n--${boundary}\r\nContent-Type: ${signatureType}\r\nContent-Transfer-Encoding: ${encoding}\r\n\r\n`),
    utf8(encoded),
    utf8(`\r\n--${boundary}--\r\n`),
  ])
  return { contentType: `multipart/signed; protocol="${protocol}"; micalg="${micalg}"; boundary="${boundary}"`, body }
}

function headerMap(raw: string) {
  const headers = new Map<string, string>()
  const unfolded = raw.replace(/\r?\n[ \t]+/g, " ")
  for (const line of unfolded.split(/\r?\n/)) {
    const index = line.indexOf(":")
    if (index < 1) continue
    headers.set(line.slice(0, index).trim().toLowerCase(), line.slice(index + 1).trim())
  }
  return headers
}

export function splitHeaders(raw: Uint8Array) {
  const source = text(raw)
  const match = /\r?\n\r?\n/.exec(source)
  if (!match) return { headers: new Map<string, string>(), body: raw }
  const head = source.slice(0, match.index)
  return { headers: headerMap(head), body: raw.subarray(match.index + match[0].length) }
}

export function contentType(headers: Map<string, string>) {
  const raw = headers.get("content-type") ?? "text/plain"
  const [type, ...rest] = raw.split(";")
  const params: Record<string, string> = {}
  for (const item of rest) {
    const [key, value] = item.split("=")
    if (key && value) params[key.trim().toLowerCase()] = value.trim().replace(/^"|"$/g, "")
  }
  return { type: type.trim().toLowerCase(), params }
}

function findBoundary(body: Uint8Array, marker: Uint8Array, from: number) {
  for (let i = from; i <= body.length - marker.length; i++) {
    let same = true
    for (let j = 0; j < marker.length; j++) if (body[i + j] !== marker[j]) {
      same = false
      break
    }
    if (same) return i
  }
  return -1
}

export function splitMultipart(body: Uint8Array, boundary: string) {
  const marker = utf8(`--${boundary}`)
  const parts: Uint8Array[] = []
  let cursor = findBoundary(body, marker, 0)
  while (cursor >= 0) {
    let start = cursor + marker.length
    if (body[start] === 45 && body[start + 1] === 45) break
    if (body[start] === 13 && body[start + 1] === 10) start += 2
    else if (body[start] === 10) start += 1
    const next = findBoundary(body, marker, start)
    if (next < 0) break
    let end = next
    if (end >= 2 && body[end - 2] === 13 && body[end - 1] === 10) end -= 2
    else if (end >= 1 && body[end - 1] === 10) end -= 1
    parts.push(body.subarray(start, end))
    cursor = next
  }
  return parts
}

export function decodeTransfer(headers: Map<string, string>, body: Uint8Array) {
  const encoding = (headers.get("content-transfer-encoding") ?? "7bit").toLowerCase()
  if (encoding === "base64") return base64ToBytes(text(body).replace(/\s/g, ""))
  if (encoding === "quoted-printable") return decodeQuotedPrintable(body)
  return body
}

function decodeQuotedPrintable(body: Uint8Array) {
  const input = text(body).replace(/=\r?\n/g, "")
  const out: number[] = []
  for (let i = 0; i < input.length; i++) {
    if (input[i] === "=" && /^[0-9A-F]{2}/i.test(input.slice(i + 1, i + 3))) {
      out.push(parseInt(input.slice(i + 1, i + 3), 16))
      i += 2
    } else out.push(input.charCodeAt(i) & 0xff)
  }
  return Uint8Array.from(out)
}

export function parseEntity(raw: Uint8Array): ParsedMail {
  const { headers, body } = splitHeaders(raw)
  return readPart(headers, body, { headers, text: "", html: "", attachments: [] })
}

function readPart(headers: Map<string, string>, body: Uint8Array, into: ParsedMail): ParsedMail {
  const decodedBody = decodeTransfer(headers, body)
  const { type, params } = contentType(headers)
  if (type.startsWith("multipart/") && params.boundary) {
    for (const piece of splitMultipart(decodedBody, params.boundary)) {
      const partHeaders = splitHeaders(piece)
      readPart(partHeaders.headers, partHeaders.body, into)
    }
    return into
  }
  const decoded = decodedBody
  const disposition = headers.get("content-disposition") ?? ""
  const filename = params.name || /filename="?([^";]+)/i.exec(disposition)?.[1] || ""
  if (/attachment/i.test(disposition) || (filename && type !== "text/plain" && type !== "text/html")) {
    into.attachments.push({ filename: filename || "attachment", contentType: type, data: decoded })
    return into
  }
  if (type === "text/plain" && !into.text) into.text = text(decoded)
  else if (type === "text/html" && !into.html) into.html = text(decoded)
  else if (type !== "text/plain" && type !== "text/html") into.attachments.push({ filename: filename || "attachment", contentType: type, data: decoded })
  return into
}

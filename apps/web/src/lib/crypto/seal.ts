import { base64ToBytes, bytesToBase64, text, utf8 } from "@/lib/crypto/bytes"
import { buildEntity, contentType, decodeTransfer, entityBytes, multipartSigned, parseEntity, pgpEncrypted, splitHeaders, splitMultipart, type MimeEntity } from "@/lib/crypto/mime"
import { decryptOpenPGP, encryptOpenPGP, signOpenPGP, verifyOpenPGP } from "@/lib/crypto/openpgp"
import { envelopSmime, openSmime, signSmime } from "@/lib/crypto/smime"

export type MailProtocol = "smime" | "openpgp"

export type SealInput = {
  text: string
  html: string
  attachments?: { filename: string; contentType: string; data: string }[]
  protocol: MailProtocol
  encrypt: boolean
  privateKey: string
  certificate?: string
  recipientKeys: string[]
}

export type OpenedMail = {
  text: string
  html: string
  attachments: { filename: string; contentType: string; data: Uint8Array }[]
  encrypted: boolean
  signed: boolean
  verified: boolean
  protocol: MailProtocol | ""
  peerKey?: string
}

function files(input: SealInput) {
  return (input.attachments ?? []).map((file) => ({ filename: file.filename, contentType: file.contentType, data: base64ToBytes(file.data) }))
}

function asSealed(entity: MimeEntity) {
  return { contentType: entity.contentType, body: bytesToBase64(entity.body) }
}

export async function sealOutgoing(input: SealInput) {
  const entity = entityBytes(buildEntity({ text: input.text, html: input.html, attachments: files(input) }))
  if (input.protocol === "openpgp") {
    if (input.encrypt) {
      const armored = await encryptOpenPGP(entity, input.recipientKeys, input.privateKey)
      return asSealed(pgpEncrypted(armored))
    }
    const signature = await signOpenPGP(entity, input.privateKey)
    return asSealed(multipartSigned("application/pgp-signature", "pgp-sha256", entity, utf8(signature), "application/pgp-signature", "7bit"))
  }
  if (!input.certificate) throw new Error("Unlock your S/MIME certificate before sending.")
  const key = base64ToBytes(input.privateKey)
  if (input.encrypt) {
    const payload = await signSmime(entity, key, input.certificate, false)
    const enveloped = await envelopSmime(payload, input.recipientKeys)
    return { contentType: 'application/pkcs7-mime; smime-type=enveloped-data; name="smime.p7m"', body: bytesToBase64(enveloped) }
  }
  const signature = await signSmime(entity, key, input.certificate, true)
  return asSealed(multipartSigned("application/pkcs7-signature", "sha-256", entity, signature, 'application/pkcs7-signature; name="smime.p7s"'))
}

function rendered(raw: Uint8Array, extra: Omit<OpenedMail, "text" | "html" | "attachments">): OpenedMail {
  const parsed = parseEntity(raw)
  return { text: parsed.text, html: parsed.html, attachments: parsed.attachments, ...extra }
}

function partText(part: Uint8Array) {
  const split = splitHeaders(part)
  return text(decodeTransfer(split.headers, split.body))
}

export async function openRaw(raw: Uint8Array, keys: { smimeKey?: string; openpgpKey?: string; smimeCerts?: string[]; openpgpKeys?: string[] }): Promise<OpenedMail> {
  const top = splitHeaders(raw)
  const { type, params } = contentType(top.headers)
  const protocol = (params.protocol ?? "").toLowerCase()
  if (type === "multipart/encrypted" && protocol === "application/pgp-encrypted" && params.boundary) {
    if (!keys.openpgpKey) throw new Error("Unlock your OpenPGP key to read this message.")
    const parts = splitMultipart(top.body, params.boundary)
    const armored = parts[1] ? partText(parts[1]) : ""
    const opened = await decryptOpenPGP(armored, keys.openpgpKey, keys.openpgpKeys ?? [])
    return rendered(opened.content, { encrypted: true, signed: opened.signed, verified: opened.verified, protocol: "openpgp" })
  }
  if (type === "multipart/signed" && params.boundary) {
    const parts = splitMultipart(top.body, params.boundary)
    const signed = parts[0] ?? new Uint8Array()
    const signature = parts[1] ? partText(parts[1]) : ""
    if (protocol === "application/pgp-signature") {
      const ok = await verifyOpenPGP(signed, signature, keys.openpgpKeys ?? [])
      return rendered(signed, { encrypted: false, signed: true, verified: ok, protocol: "openpgp" })
    }
    const der = parts[1] ? decodeTransfer(splitHeaders(parts[1]).headers, splitHeaders(parts[1]).body) : new Uint8Array()
    const opened = await openSmime(der, keys.smimeKey ? base64ToBytes(keys.smimeKey) : undefined, signed)
    return rendered(signed, { encrypted: false, signed: true, verified: opened.verified, protocol: "smime", peerKey: opened.certificate })
  }
  if (type === "application/pkcs7-mime" || type === "application/x-pkcs7-mime") {
    const der = decodeTransfer(top.headers, top.body)
    const opened = await openSmime(der, keys.smimeKey ? base64ToBytes(keys.smimeKey) : undefined)
    return rendered(opened.content, { encrypted: true, signed: opened.verified, verified: opened.verified, protocol: "smime", peerKey: opened.certificate })
  }
  return rendered(raw, { encrypted: false, signed: false, verified: false, protocol: "" })
}

import { asBuffer, base64ToBytes, bytesToBase64, concat, randomBytes, randomHex, sha256, utf8 } from "@/lib/crypto/bytes"

const iterations = 210_000

export type ProtectedPayload = { subject: string; text: string; html: string; replyProof: string }
export type ProtectedReply = { text: string; html: string }

async function passwordKey(password: string, salt: Uint8Array) {
  const material = await crypto.subtle.importKey("raw", utf8(password), "PBKDF2", false, ["deriveKey"])
  return crypto.subtle.deriveKey({ name: "PBKDF2", salt: asBuffer(salt), iterations, hash: "SHA-256" }, material, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"])
}

async function seal(password: string, salt: Uint8Array, aad: string, value: object) {
  const nonce = randomBytes(12)
  const key = await passwordKey(password, salt)
  const ciphertext = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv: asBuffer(nonce), additionalData: asBuffer(utf8(aad)) }, key, asBuffer(utf8(JSON.stringify(value)))))
  return concat([Uint8Array.of(1), nonce, ciphertext])
}

async function open(password: string, salt: Uint8Array, aad: string, message: Uint8Array) {
  if (message[0] !== 1 || message.length < 29) throw new Error("This protected message cannot be read.")
  const key = await passwordKey(password, salt)
  try {
    const plain = await crypto.subtle.decrypt({ name: "AES-GCM", iv: asBuffer(message.subarray(1, 13)), additionalData: asBuffer(utf8(aad)) }, key, asBuffer(message.subarray(13)))
    return JSON.parse(new TextDecoder().decode(plain))
  } catch {
    throw new Error("That password does not open this message.")
  }
}

export async function sealProtected(password: string, id: string, payload: Omit<ProtectedPayload, "replyProof">) {
  const salt = randomBytes(16)
  const access = randomHex(32)
  const replyProof = randomHex(32)
  const message = await seal(password, salt, id, { ...payload, replyProof })
  return { access, replyHash: bytesToBase64(await sha256(replyProof)), salt: bytesToBase64(salt), message: bytesToBase64(message) }
}

export async function openProtected(password: string, id: string, salt: string, message: string) {
  return (await open(password, base64ToBytes(salt), id, base64ToBytes(message))) as ProtectedPayload
}

export async function sealReply(password: string, id: string, salt: string, reply: ProtectedReply) {
  return bytesToBase64(await seal(password, base64ToBytes(salt), `${id}.reply`, reply))
}

export async function openReply(password: string, id: string, salt: string, message: string) {
  return (await open(password, base64ToBytes(salt), `${id}.reply`, base64ToBytes(message))) as ProtectedReply
}

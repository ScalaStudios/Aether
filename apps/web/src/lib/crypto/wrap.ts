import { asBuffer, concat, randomBytes, utf8 } from "@/lib/crypto/bytes"

export const wrapIterations = 210_000

async function wrappingKey(passphrase: string, salt: Uint8Array, iterations: number) {
  const material = await crypto.subtle.importKey("raw", utf8(passphrase), "PBKDF2", false, ["deriveKey"])
  return crypto.subtle.deriveKey({ name: "PBKDF2", salt: asBuffer(salt), iterations, hash: "SHA-256" }, material, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"])
}

export async function wrapPrivateKey(privateKey: Uint8Array, passphrase: string) {
  const salt = randomBytes(16)
  const iv = randomBytes(12)
  const key = await wrappingKey(passphrase, salt, wrapIterations)
  const ciphertext = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv: asBuffer(iv) }, key, asBuffer(privateKey)))
  return { privateKey: concat([Uint8Array.of(0xa1), iv, ciphertext]), salt, iterations: wrapIterations }
}

export async function unwrapPrivateKey(blob: Uint8Array, salt: Uint8Array, iterations: number, passphrase: string) {
  if (blob[0] !== 0xa1 || blob.length < 29 || iterations < 200_000) throw new Error("This key is not stored in Aether's encrypted form.")
  const key = await wrappingKey(passphrase, salt, iterations)
  try {
    return new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: asBuffer(blob.subarray(1, 13)) }, key, asBuffer(blob.subarray(13))))
  } catch {
    throw new Error("That passphrase does not unlock this key.")
  }
}

export function download(filename: string, bytes: Uint8Array, type: string) {
  const blob = new Blob([Uint8Array.from(bytes)], { type })
  const url = URL.createObjectURL(blob)
  const link = document.createElement("a")
  link.href = url
  link.download = filename
  link.click()
  URL.revokeObjectURL(url)
}

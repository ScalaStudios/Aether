import { base64ToBytes, bytesToBase64, text, utf8 } from "@/lib/crypto/bytes"
import { unwrapPrivateKey } from "@/lib/crypto/wrap"

export type UnlockedKey = { id: string; protocol: "smime" | "openpgp"; address: string; fingerprint: string; privateKey: string }

const storageKey = "aether.mail-keys"
const emptyKeys: UnlockedKey[] = []
let cacheToken = "\n"
let cacheKeys: UnlockedKey[] = emptyKeys
const listeners = new Set<() => void>()

function notify() {
  for (const listener of listeners) listener()
}

export function subscribeKeys(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function unlockedKeys() {
  if (typeof sessionStorage === "undefined") return emptyKeys
  const raw = sessionStorage.getItem(storageKey) ?? ""
  const user = sessionStorage.getItem("aether.mail-user") ?? ""
  const token = `${user}\n${raw}`
  if (token === cacheToken) return cacheKeys
  cacheToken = token
  try {
    const parsed = JSON.parse(raw || "[]") as { userId: string; keys: UnlockedKey[] }
    cacheKeys = parsed.userId === user && Array.isArray(parsed.keys) ? parsed.keys : emptyKeys
  } catch {
    cacheKeys = emptyKeys
  }
  return cacheKeys
}

export function lockKeys() {
  sessionStorage.removeItem(storageKey)
  sessionStorage.removeItem("aether.mail-user")
  notify()
}

export function rememberUser(userId: string) {
  const current = sessionStorage.getItem("aether.mail-user")
  if (current && current !== userId) lockKeys()
  sessionStorage.setItem("aether.mail-user", userId)
}

export async function unlockKeys(userId: string, passphrase: string, keys: { id: string; protocol: "smime" | "openpgp"; address: string; fingerprint: string; privateKey: string; salt: string; iterations: number }[]) {
  rememberUser(userId)
  const opened: UnlockedKey[] = []
  for (const key of keys) {
    const secret = await unwrapPrivateKey(base64ToBytes(key.privateKey), base64ToBytes(key.salt), key.iterations, passphrase)
    opened.push({ id: key.id, protocol: key.protocol, address: key.address, fingerprint: key.fingerprint, privateKey: key.protocol === "openpgp" ? text(secret) : bytesToBase64(secret) })
  }
  sessionStorage.setItem(storageKey, JSON.stringify({ userId, keys: opened }))
  notify()
  return opened
}

export function keyFor(address: string, protocol: "smime" | "openpgp") {
  const wanted = address.toLowerCase()
  return unlockedKeys().find((key) => key.protocol === protocol && key.address.toLowerCase() === wanted)
}

export function privateBytes(key: UnlockedKey) {
  return key.protocol === "openpgp" ? utf8(key.privateKey) : base64ToBytes(key.privateKey)
}

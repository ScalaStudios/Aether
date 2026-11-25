export function concat(parts: Uint8Array[]) {
  const out = new Uint8Array(parts.reduce((n, part) => n + part.length, 0))
  let offset = 0
  for (const part of parts) {
    out.set(part, offset)
    offset += part.length
  }
  return out
}

export function utf8(value: string) {
  return new TextEncoder().encode(value)
}

export function text(bytes: Uint8Array) {
  return new TextDecoder().decode(bytes)
}

export function bytesToBase64(bytes: Uint8Array) {
  let binary = ""
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  return btoa(binary)
}

export function base64ToBytes(value: string) {
  const binary = atob(value)
  const out = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i)
  return out
}

export function bytesToBinary(bytes: Uint8Array) {
  let binary = ""
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  return binary
}

export function binaryToBytes(value: string) {
  const out = new Uint8Array(value.length)
  for (let i = 0; i < value.length; i++) out[i] = value.charCodeAt(i) & 0xff
  return out
}

export function asBuffer(bytes: Uint8Array): ArrayBuffer {
  const out = new ArrayBuffer(bytes.byteLength)
  new Uint8Array(out).set(bytes)
  return out
}

export function randomBytes(size: number) {
  return crypto.getRandomValues(new Uint8Array(size))
}

export function randomHex(size: number) {
  return [...randomBytes(size)].map((b) => b.toString(16).padStart(2, "0")).join("")
}

export async function sha256(value: string) {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", utf8(value)))
}

const words = [
  "amber", "basin", "cedar", "delta", "ember", "fjord", "glen", "harbor", "ivory", "juniper", "kelp", "lantern", "maple", "north", "olive", "pebble",
  "quartz", "river", "spruce", "thistle", "umber", "violet", "willow", "yarrow", "zephyr", "anchor", "birch", "cinder", "drift", "elm", "flint", "grove",
  "hazel", "ink", "jasper", "kite", "lark", "meadow", "nest", "orchard", "pine", "quill", "reef", "sage", "timber", "upland", "vine", "wheat",
  "aspen", "brook", "clover", "dune", "fern", "heron", "iris", "lichen", "moss", "otter", "poppy", "ridge", "stone", "trail",
]

export function readableSecret() {
  const pick = (n: number) => words[n % words.length]
  const bytes = randomBytes(4)
  return `${pick(bytes[0])}-${pick(bytes[1])}-${pick(bytes[2])}-${10 + (bytes[3] % 90)}`
}

export function formatFingerprint(value: string) {
  return value
    .replace(/[^0-9a-f]/gi, "")
    .toUpperCase()
    .replace(/(.{4})/g, "$1 ")
    .trim()
}

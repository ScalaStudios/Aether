import { asBuffer, base64ToBytes, binaryToBytes, bytesToBinary } from "@/lib/crypto/bytes"

type Bytes = string

interface Asn1 {
  tagClass: number
  type: number
  constructed: boolean
  value: Asn1[] | Bytes
}

interface Digest {
  update(data: Bytes, encoding?: "raw" | "utf8"): Digest
  digest(): { getBytes(): Bytes }
}

interface RsaKey {
  n: { compareTo?(other: unknown): number; toString(radix?: number): string }
  e: unknown
  sign(md: Digest, scheme: string): Bytes
  verify(digest: Bytes, signature: Bytes): boolean
}

interface Certificate {
  publicKey: RsaKey
  subject: { attributes: { shortName?: string; name?: string; value?: unknown }[] }
  getExtension(name: string): { altNames?: { type: number; value?: string }[] } | null
}

interface Pkcs7 {
  type: string
  content?: { getBytes(): Bytes } | Bytes
  certificates: Certificate[]
  recipients: unknown[]
  rawCapture: { signature?: Bytes; digestAlgorithm?: Bytes; authenticatedAttributes?: Asn1[] | Bytes; content?: Asn1 }
  addCertificate(cert: Certificate): void
  addSigner(signer: { key: RsaKey; certificate: Certificate; digestAlgorithm: string; authenticatedAttributes: { type: string; value?: string }[] }): void
  addRecipient(cert: Certificate): void
  sign(options?: { detached?: boolean }): void
  encrypt(): void
  decrypt(recipient: unknown, key: RsaKey): void
  toAsn1(): Asn1
}

interface Forge {
  asn1: {
    Class: { UNIVERSAL: number }
    Type: { SET: number; OID: number; OCTETSTRING: number }
    fromDer(input: Bytes): Asn1
    toDer(obj: Asn1): { getBytes(): Bytes }
    derToOid(bytes: Bytes): string
    create(tagClass: number, type: number, constructed: boolean, value: Asn1[]): Asn1
  }
  md: {
    sha1: { create(): Digest }
    sha256: { create(): Digest }
    sha384: { create(): Digest }
    sha512: { create(): Digest }
  }
  pem: { encode(msg: { type: string; body: Bytes }): string }
  pki: {
    oids: Record<string, string>
    certificateFromPem(pem: string): Certificate
    certificateToAsn1(cert: Certificate): Asn1
    certificateFromAsn1(obj: Asn1): Certificate
    privateKeyFromPem(pem: string): RsaKey
    privateKeyFromAsn1(obj: Asn1): RsaKey
    privateKeyToAsn1(key: RsaKey): Asn1
    wrapRsaPrivateKey(key: Asn1): Asn1
  }
  pkcs7: { messageFromAsn1(obj: Asn1): Pkcs7; createSignedData(): Pkcs7; createEnvelopedData(): Pkcs7 }
  pkcs12: { pkcs12FromAsn1(obj: Asn1, password: string): { getBags(filter: { bagType: string }): Record<string, { key?: RsaKey; cert?: Certificate }[]> } }
  util: { createBuffer(bytes: Bytes): { getBytes(): Bytes } }
}

export type SmimeMaterial = { privateKey: Uint8Array; certificate: string; fingerprint: string; emails: string[] }

async function forge() {
  const mod = await import("node-forge")
  const loaded = ((mod as unknown as { default?: Forge }).default ?? (mod as unknown as Forge))
  if (!loaded.pki) throw new Error("S/MIME support could not be loaded.")
  return loaded
}

function rsa(key: RsaKey) {
  if (!key?.n) throw new Error("Aether signs and decrypts S/MIME with RSA keys. This key uses another algorithm.")
  return key
}

function certificateEmails(cert: Certificate) {
  const emails = new Set<string>()
  for (const name of cert.getExtension("subjectAltName")?.altNames ?? []) {
    if (name.type === 1 && name.value?.includes("@")) emails.add(name.value.toLowerCase())
  }
  for (const field of cert.subject.attributes) {
    const value = typeof field.value === "string" ? field.value : ""
    if ((field.shortName === "E" || field.name === "emailAddress") && value.includes("@")) emails.add(value.toLowerCase())
  }
  return [...emails]
}

async function fingerprintOf(der: Uint8Array) {
  const digest = await crypto.subtle.digest("SHA-256", asBuffer(der))
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("")
}

function pemDer(pem: string) {
  const body = pem.replace(/-----BEGIN [^-]+-----/g, "").replace(/-----END [^-]+-----/g, "").replace(/\s/g, "")
  return base64ToBytes(body)
}

function pemCertificate(f: Forge, cert: Certificate) {
  return f.pem.encode({ type: "CERTIFICATE", body: f.asn1.toDer(f.pki.certificateToAsn1(cert)).getBytes() })
}

function pkcs8(f: Forge, key: RsaKey) {
  return binaryToBytes(f.asn1.toDer(f.pki.wrapRsaPrivateKey(f.pki.privateKeyToAsn1(rsa(key)))).getBytes())
}

function material(f: Forge, key: RsaKey, cert: Certificate): SmimeMaterial {
  const emails = certificateEmails(cert)
  if (!emails.length) throw new Error("This certificate has no email address. Use a certificate issued for the mailbox.")
  return { privateKey: pkcs8(f, key), certificate: pemCertificate(f, cert), fingerprint: "", emails }
}

export async function importSmimePkcs12(file: Uint8Array, password: string) {
  const f = await forge()
  let p12
  try {
    p12 = f.pkcs12.pkcs12FromAsn1(f.asn1.fromDer(bytesToBinary(file)), password)
  } catch {
    throw new Error("That file could not be opened. Check the password and choose a PKCS#12 file.")
  }
  const shrouded = f.pki.oids.pkcs8ShroudedKeyBag
  const plain = f.pki.oids.keyBag
  const key = p12.getBags({ bagType: shrouded })[shrouded]?.[0]?.key ?? p12.getBags({ bagType: plain })[plain]?.[0]?.key
  if (!key) throw new Error("The file does not contain a private key.")
  const certs = (p12.getBags({ bagType: f.pki.oids.certBag })[f.pki.oids.certBag] ?? []).map((bag) => bag.cert).filter((cert): cert is Certificate => !!cert)
  const cert = certs.find((item) => item.publicKey?.n && key.n && item.publicKey.n.toString(16) === key.n.toString(16)) ?? certs[0]
  if (!cert) throw new Error("The file does not contain a certificate.")
  const imported = material(f, key, cert)
  imported.fingerprint = await fingerprintOf(pemDer(imported.certificate))
  return imported
}

export async function importSmimePem(certificatePem: string, privateKeyPem: string) {
  const f = await forge()
  let cert: Certificate
  let key: RsaKey
  try {
    cert = f.pki.certificateFromPem(certificatePem)
    key = f.pki.privateKeyFromPem(privateKeyPem)
  } catch {
    throw new Error("Paste an unencrypted RSA certificate and private key.")
  }
  if (cert.publicKey?.n?.toString(16) !== key.n?.toString(16)) throw new Error("The private key does not match this certificate.")
  const imported = material(f, key, cert)
  imported.certificate = certificatePem.trim().endsWith("\n") ? certificatePem.trim() + "\n" : `${certificatePem.trim()}\n`
  imported.fingerprint = await fingerprintOf(pemDer(imported.certificate))
  return imported
}

function attributes(f: Forge) {
  return [
    { type: f.pki.oids.contentType, value: f.pki.oids.data },
    { type: f.pki.oids.messageDigest },
    { type: f.pki.oids.signingTime },
  ]
}

function der(f: Forge, msg: Pkcs7) {
  return binaryToBytes(f.asn1.toDer(msg.toAsn1()).getBytes())
}

export async function signSmime(entity: Uint8Array, privateKey: Uint8Array, certificatePem: string, detached: boolean) {
  const f = await forge()
  const key = rsa(f.pki.privateKeyFromAsn1(f.asn1.fromDer(bytesToBinary(privateKey))))
  const cert = f.pki.certificateFromPem(certificatePem)
  const signed = f.pkcs7.createSignedData()
  signed.content = f.util.createBuffer(bytesToBinary(entity))
  signed.addCertificate(cert)
  signed.addSigner({ key, certificate: cert, digestAlgorithm: f.pki.oids.sha256, authenticatedAttributes: attributes(f) })
  signed.sign(detached ? { detached: true } : undefined)
  return der(f, signed)
}

export async function envelopSmime(content: Uint8Array, certificates: string[]) {
  const f = await forge()
  const enveloped = f.pkcs7.createEnvelopedData()
  enveloped.content = f.util.createBuffer(bytesToBinary(content))
  if (!certificates.length) throw new Error("Add an S/MIME certificate for each recipient.")
  for (const pem of certificates) enveloped.addRecipient(f.pki.certificateFromPem(pem))
  enveloped.encrypt()
  return der(f, enveloped)
}

function digestFor(f: Forge, oid: string) {
  if (oid === f.pki.oids.sha256) return f.md.sha256.create()
  if (oid === f.pki.oids.sha1) return f.md.sha1.create()
  if (oid === f.pki.oids.sha384) return f.md.sha384.create()
  if (oid === f.pki.oids.sha512) return f.md.sha512.create()
  return null
}

function collectOctets(node: Asn1): Bytes {
  if (!Array.isArray(node.value)) return node.type === 4 ? node.value : ""
  return node.value.map(collectOctets).join("")
}

function contentBytes(msg: Pkcs7) {
  const direct = !msg.content ? "" : typeof msg.content === "string" ? msg.content : msg.content.getBytes()
  if (direct) return direct
  return msg.rawCapture.content ? collectOctets(msg.rawCapture.content) : ""
}

function attributeValue(f: Forge, nodes: Asn1[], oid: string) {
  for (const node of nodes) {
    if (!Array.isArray(node.value) || node.value.length < 2) continue
    const id = node.value[0]
    if (!id || Array.isArray(id.value) || f.asn1.derToOid(id.value) !== oid) continue
    const set = node.value[1]
    if (!set || !Array.isArray(set.value)) continue
    const oct = set.value[0]
    if (oct && !Array.isArray(oct.value)) return oct.value
  }
  return null
}

function verifySignature(f: Forge, msg: Pkcs7, content: Bytes) {
  const signature = msg.rawCapture.signature
  const digestOid = msg.rawCapture.digestAlgorithm ? f.asn1.derToOid(msg.rawCapture.digestAlgorithm) : ""
  const hasher = digestFor(f, digestOid)
  if (!signature || !hasher) return null
  const contentHash = digestFor(f, digestOid)!
  contentHash.update(content, "raw")
  const digest = contentHash.digest().getBytes()
  let signed = digest
  const attrs = msg.rawCapture.authenticatedAttributes
  if (Array.isArray(attrs) && attrs.length) {
    if (attributeValue(f, attrs, f.pki.oids.messageDigest) !== digest) return null
    const set = f.asn1.create(f.asn1.Class.UNIVERSAL, f.asn1.Type.SET, true, attrs)
    const attrHash = digestFor(f, digestOid)!
    attrHash.update(f.asn1.toDer(set).getBytes(), "raw")
    signed = attrHash.digest().getBytes()
  }
  for (const cert of msg.certificates ?? []) {
    try {
      if (cert.publicKey.verify(signed, signature)) return cert
    } catch {
      continue
    }
  }
  return null
}

export type SmimeOpen = { content: Uint8Array; verified: boolean; certificate?: string }

export async function openSmime(derBytes: Uint8Array, privateKey: Uint8Array | undefined, detached?: Uint8Array): Promise<SmimeOpen> {
  const f = await forge()
  let msg: Pkcs7
  try {
    msg = f.pkcs7.messageFromAsn1(f.asn1.fromDer(bytesToBinary(derBytes)))
  } catch {
    throw new Error("This S/MIME message could not be read.")
  }
  if (msg.type === f.pki.oids.envelopedData) {
    if (!privateKey) throw new Error("Unlock your S/MIME key to read this message.")
    const key = rsa(f.pki.privateKeyFromAsn1(f.asn1.fromDer(bytesToBinary(privateKey))))
    let opened = false
    for (const recipient of msg.recipients) {
      try {
        msg.decrypt(recipient, key)
        opened = true
        break
      } catch {
        continue
      }
    }
    if (!opened) throw new Error("This message was not encrypted to your S/MIME key.")
    const inner = binaryToBytes(contentBytes(msg))
    if (inner[0] === 0x30) {
      try {
        return await openSmime(inner, privateKey)
      } catch {
        return { content: inner, verified: false }
      }
    }
    return { content: inner, verified: false }
  }
  const content = detached ? bytesToBinary(detached) : contentBytes(msg)
  const cert = verifySignature(f, msg, content)
  return { content: binaryToBytes(content), verified: !!cert, certificate: cert ? pemCertificate(f, cert) : undefined }
}

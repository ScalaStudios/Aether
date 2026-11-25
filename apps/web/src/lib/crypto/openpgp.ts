import { base64ToBytes, bytesToBase64 } from "@/lib/crypto/bytes"

type Pgp = typeof import("openpgp")
type PublicKey = import("openpgp").Key
type PrivateKey = import("openpgp").PrivateKey

async function pgp() {
  return import("openpgp") as Promise<Pgp>
}

function emailsFromUserIds(ids: string[]) {
  const emails = new Set<string>()
  for (const id of ids) {
    const wrapped = /<([^<>\s]+@[^<>\s]+)>/.exec(id)?.[1]
    const bare = id.trim()
    const email = wrapped || (bare.includes("@") && !/[\s<>]/.test(bare) ? bare : "")
    if (email) emails.add(email.toLowerCase())
  }
  return [...emails]
}

async function describe(key: PublicKey) {
  const pub = key.isPrivate() ? key.toPublic() : key
  return { fingerprint: pub.getFingerprint().toLowerCase(), emails: emailsFromUserIds(pub.getUserIDs()), publicKey: pub.armor() }
}

export async function generateOpenPGP(name: string, email: string) {
  const { generateKey, readPrivateKey } = await pgp()
  const generated = await generateKey({ type: "ecc", curve: "ed25519Legacy", userIDs: [{ name: name || email, email }], format: "armored" })
  const key = await readPrivateKey({ armoredKey: generated.privateKey })
  const info = await describe(key)
  if (!info.emails.includes(email.toLowerCase())) throw new Error("The new key is missing this email address.")
  return { privateKey: generated.privateKey, ...info }
}

export async function importOpenPGP(armored: string, passphrase = "") {
  const { readPrivateKey, decryptKey } = await pgp()
  let key: PrivateKey
  try {
    key = await readPrivateKey({ armoredKey: armored })
  } catch {
    throw new Error("This is not an OpenPGP private key.")
  }
  if (!key.isDecrypted()) {
    if (!passphrase) throw new Error("Enter the passphrase for this key.")
    try {
      key = await decryptKey({ privateKey: key, passphrase })
    } catch {
      throw new Error("That passphrase does not unlock this key.")
    }
  }
  const info = await describe(key)
  if (!info.emails.length) throw new Error("This key has no email address.")
  return { privateKey: key.armor(), ...info }
}

export async function autocryptHeader(address: string, armoredPrivate: string) {
  const { readPrivateKey } = await pgp()
  const key = await readPrivateKey({ armoredKey: armoredPrivate })
  return `addr=${address.toLowerCase()}; prefer-encrypt=mutual; keydata=${bytesToBase64(key.toPublic().write())}`
}

export async function readAutocrypt(header: string) {
  const fields = new Map(header.split(";").map((part) => {
    const [key, ...rest] = part.trim().split("=")
    return [key.trim().toLowerCase(), rest.join("=").trim()] as const
  }))
  const data = fields.get("keydata")
  if (!data) return null
  const { readKey } = await pgp()
  try {
    const key = await readKey({ binaryKey: base64ToBytes(data) })
    return { address: (fields.get("addr") ?? "").toLowerCase(), ...(await describe(key)) }
  } catch {
    return null
  }
}

async function privateKey(armored: string) {
  const { readPrivateKey } = await pgp()
  const key = await readPrivateKey({ armoredKey: armored })
  if (!key.isDecrypted()) throw new Error("Unlock your OpenPGP key to protect this message.")
  return key
}

async function publicKeys(armored: string[]) {
  const { readKey } = await pgp()
  return Promise.all(armored.map((item) => readKey({ armoredKey: item })))
}

export async function encryptOpenPGP(entity: Uint8Array, recipients: string[], signer?: string) {
  const { createMessage, encrypt } = await pgp()
  if (!recipients.length) throw new Error("Add an OpenPGP key for each recipient.")
  const message = await createMessage({ binary: entity })
  return encrypt({
    message,
    encryptionKeys: await publicKeys(recipients),
    signingKeys: signer ? await privateKey(signer) : undefined,
    format: "armored",
  })
}

export async function signOpenPGP(entity: Uint8Array, signer: string) {
  const { createMessage, sign } = await pgp()
  const message = await createMessage({ binary: entity })
  return sign({ message, signingKeys: await privateKey(signer), detached: true, format: "armored" })
}

async function verified(signatures: { verified: Promise<true> }[]) {
  const checks = await Promise.all(signatures.map(async (signature) => {
    try {
      await signature.verified
      return true
    } catch {
      return false
    }
  }))
  return checks.some(Boolean)
}

export async function decryptOpenPGP(armored: string, privateArmored: string, verificationKeys: string[]) {
  const { readMessage, decrypt } = await pgp()
  const message = await readMessage({ armoredMessage: armored })
  const result = await decrypt({
    message,
    decryptionKeys: await privateKey(privateArmored),
    verificationKeys: verificationKeys.length ? await publicKeys(verificationKeys) : undefined,
    format: "binary",
  })
  if (!(result.data instanceof Uint8Array)) throw new Error("This OpenPGP message could not be read.")
  return { content: result.data, signed: result.signatures.length > 0, verified: await verified(result.signatures) }
}

export async function verifyOpenPGP(entity: Uint8Array, signatureText: string, verificationKeys: string[]) {
  if (!verificationKeys.length) return false
  const { createMessage, readSignature, verify } = await pgp()
  try {
    const result = await verify({
      message: await createMessage({ binary: entity }),
      signature: await readSignature({ armoredSignature: signatureText }),
      verificationKeys: await publicKeys(verificationKeys),
      format: "binary",
    })
    return verified(result.signatures)
  } catch {
    return false
  }
}

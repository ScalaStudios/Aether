interface Descriptor {
  type: "public-key"
  id: string
  transports: AuthenticatorTransport[]
}

export interface SecurityKeyCreationOptions {
  challenge: string
  rp: { id: string; name: string }
  user: { id: string; name: string; displayName: string }
  pubKeyCredParams: PublicKeyCredentialParameters[]
  timeout: number
  attestation: AttestationConveyancePreference
  authenticatorSelection: AuthenticatorSelectionCriteria
  excludeCredentials: Descriptor[]
}

export interface SecurityKeyRequestOptions {
  challenge: string
  rpId: string
  allowCredentials: Descriptor[]
  timeout: number
  userVerification: UserVerificationRequirement
}

const toBytes = (value: string) => Uint8Array.from(atob(value), (c) => c.charCodeAt(0))
const toBase64 = (buffer: ArrayBuffer) => btoa(String.fromCharCode(...new Uint8Array(buffer)))
const descriptors = (list: Descriptor[]) => list.map((d) => ({ ...d, id: toBytes(d.id) }))

export const securityKeysSupported = () => typeof window !== "undefined" && "PublicKeyCredential" in window

async function ask<T extends AuthenticatorResponse>(request: Promise<Credential | null>) {
  try {
    const credential = (await request) as PublicKeyCredential | null
    if (!credential) throw new Error()
    return { id: toBase64(credential.rawId), response: credential.response as T }
  } catch (error) {
    if (error instanceof DOMException && error.name === "InvalidStateError") throw new Error("This security key is already registered.")
    throw new Error("The security key request was cancelled or timed out. Try again.")
  }
}

export async function createSecurityKey(options: SecurityKeyCreationOptions) {
  const { id, response } = await ask<AuthenticatorAttestationResponse>(
    navigator.credentials.create({
      publicKey: { ...options, challenge: toBytes(options.challenge), user: { ...options.user, id: toBytes(options.user.id) }, excludeCredentials: descriptors(options.excludeCredentials) },
    })
  )
  const publicKey = response.getPublicKey()
  if (!publicKey) throw new Error("This browser cannot read the security key. Update the browser, or use another one.")
  return {
    id,
    clientDataJSON: toBase64(response.clientDataJSON),
    authenticatorData: toBase64(response.getAuthenticatorData()),
    publicKey: toBase64(publicKey),
    transports: response.getTransports(),
  }
}

export async function getSecurityKey(options: SecurityKeyRequestOptions) {
  const { id, response } = await ask<AuthenticatorAssertionResponse>(
    navigator.credentials.get({ publicKey: { ...options, challenge: toBytes(options.challenge), allowCredentials: descriptors(options.allowCredentials) } })
  )
  return {
    id,
    clientDataJSON: toBase64(response.clientDataJSON),
    authenticatorData: toBase64(response.authenticatorData),
    signature: toBase64(response.signature),
  }
}

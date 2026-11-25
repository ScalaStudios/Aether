"use client"

import { Field } from "@/components/field"
import { SegmentedControl } from "@/components/segmented-control"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { readableSecret } from "@/lib/crypto/bytes"
import type { EncryptionPolicy } from "@/lib/types"

export type ProtectionMode = "standard" | "sign" | "encrypt" | "password"
export type ProtectionProtocol = "smime" | "openpgp"

const modes = [
  { value: "standard" as const, label: "Standard" },
  { value: "sign" as const, label: "Sign" },
  { value: "encrypt" as const, label: "Encrypt" },
  { value: "password" as const, label: "Password" },
]

export function ProtectionControls({
  policy,
  mode,
  protocol,
  password,
  hours,
  autocrypt,
  canPassword,
  onMode,
  onProtocol,
  onPassword,
  onHours,
  onAutocrypt,
}: {
  policy?: EncryptionPolicy
  mode: ProtectionMode
  protocol: ProtectionProtocol
  password: string
  hours: 24 | 72 | 168
  autocrypt: boolean
  canPassword: boolean
  onMode: (mode: ProtectionMode) => void
  onProtocol: (protocol: ProtectionProtocol) => void
  onPassword: (password: string) => void
  onHours: (hours: 24 | 72 | 168) => void
  onAutocrypt: (value: boolean) => void
}) {
  const options = modes.filter((item) => {
    if (item.value === "standard") return true
    if (item.value === "password") return canPassword && policy?.password !== "off"
    if (item.value === "sign" || item.value === "encrypt") return policy?.smime !== "off" || policy?.openpgp !== "off"
    return false
  })
  if (!policy || options.length < 2) return null
  const protocols = [
    policy.smime !== "off" ? { value: "smime" as const, label: "S/MIME" } : null,
    policy.openpgp !== "off" ? { value: "openpgp" as const, label: "OpenPGP" } : null,
  ].filter((item): item is { value: ProtectionProtocol; label: string } => !!item)
  const required = [policy.smime === "require" ? "S/MIME" : "", policy.openpgp === "require" ? "OpenPGP" : ""].filter(Boolean)
  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="font-semibold">Protection</p>
        <SegmentedControl label="Protection" options={options} value={mode} onChange={onMode} className="max-sm:w-full max-sm:flex-wrap" />
      </div>
      {required.length > 0 && mode === "standard" && (
        <p className="text-sm text-fg-secondary">Your organization requires outgoing mail to be encrypted with {required.join(" or ")}.</p>
      )}
      {policy.password === "require" && mode === "standard" && (
        <p className="text-sm text-fg-secondary">Recipients outside your domains need a password-protected message, or mail encrypted to their key.</p>
      )}
      {(mode === "sign" || mode === "encrypt") && protocols.length > 1 && (
        <SegmentedControl label="Protocol" options={protocols} value={protocol} onChange={onProtocol} />
      )}
      {mode === "encrypt" && <p className="text-sm text-fg-tertiary">Encrypted and signed in this browser. The message is not saved as a draft.</p>}
      {mode === "sign" && <p className="text-sm text-fg-tertiary">Signed in this browser. The message is not saved as a draft.</p>}
      {mode === "password" && (
        <div className="flex flex-col gap-3">
          <Field id="compose-password" label="Password" hint="The recipient gets a link by email. Send this password another way. Aether does not store it.">
            <div className="flex gap-2">
              <Input id="compose-password" value={password} onChange={(event) => onPassword(event.target.value)} autoComplete="off" className="font-mono" />
              <Button type="button" variant="secondary" onClick={() => onPassword(readableSecret())}>New</Button>
            </div>
          </Field>
          <div className="flex flex-col gap-2">
            <p className="text-sm font-semibold">Link expires</p>
            <SegmentedControl label="Link expiry" options={[{ value: 24, label: "1 day" }, { value: 72, label: "3 days" }, { value: 168, label: "7 days" }]} value={hours} onChange={onHours} />
          </div>
        </div>
      )}
      {(mode === "sign" || mode === "encrypt") && protocol === "openpgp" && policy.openpgp !== "off" && (
        <label className="flex items-start gap-3 text-sm">
          <input type="checkbox" className="mt-1 size-4 accent-ember" checked={autocrypt} onChange={(event) => onAutocrypt(event.target.checked)} />
          <span>Include my public key so they can encrypt a reply. It is sent as an Autocrypt header, not as the private key.</span>
        </label>
      )}
    </div>
  )
}

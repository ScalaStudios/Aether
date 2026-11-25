"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { KeyRound, Lock, LockOpen, Plus, Trash2, Upload } from "lucide-react"
import { useEffect, useState, useSyncExternalStore } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { bytesToBase64, formatFingerprint, text, utf8 } from "@/lib/crypto/bytes"
import { generateOpenPGP, importOpenPGP } from "@/lib/crypto/openpgp"
import { keyFor, lockKeys, rememberUser, subscribeKeys, unlockKeys, unlockedKeys } from "@/lib/crypto/session"
import { importSmimePem, importSmimePkcs12 } from "@/lib/crypto/smime"
import { download, unwrapPrivateKey, wrapPrivateKey } from "@/lib/crypto/wrap"
import { myMailboxesQuery } from "@/lib/queries"
import type { EncryptionKey, EncryptionPolicy, Mailbox } from "@/lib/types"

const protocols = [
  { value: "smime" as const, label: "S/MIME" },
  { value: "openpgp" as const, label: "OpenPGP" },
]

function pem(type: string, der: Uint8Array) {
  const body = bytesToBase64(der).replace(/(.{64})/g, "$1\n")
  return `-----BEGIN ${type}-----\n${body}\n-----END ${type}-----\n`
}

async function fileBytes(file: File) {
  return new Uint8Array(await file.arrayBuffer())
}

export function Encryption() {
  const session = useSession().data
  const mailboxes = useQuery(myMailboxesQuery)
  const keys = useQuery({ queryKey: ["encryption-keys"], queryFn: () => api<EncryptionKey[]>("/me/encryption-keys") })
  const unlocked = useSyncExternalStore(subscribeKeys, unlockedKeys, unlockedKeys)
  const identities = (mailboxes.data ?? []).filter((m) => m.access === "owner" || m.sendAs)
  const [unlocking, setUnlocking] = useState("")
  const [importing, setImporting] = useState(false)
  const [generating, setGenerating] = useState(false)
  const [removing, setRemoving] = useState<EncryptionKey | null>(null)
  const qc = useQueryClient()
  const remove = useMutation({
    mutationFn: (key: EncryptionKey) => api(`/me/encryption-keys/${key.id}`, { method: "DELETE" }),
    onSuccess: (_data, key) => {
      if (keyFor(key.address, key.protocol)) lockKeys()
      toast.success("Key removed")
      setRemoving(null)
      qc.invalidateQueries({ queryKey: ["encryption-keys"] })
    },
  })

  useEffect(() => {
    if (session) rememberUser(session.user.id)
  }, [session])

  return (
    <section id="encryption" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader
        title="Encryption"
        description="Sign and encrypt mail with S/MIME or OpenPGP. The private key is encrypted in this browser before it is saved. Aether never receives it, or the passphrase that unlocks it."
        actions={
          <div className="flex flex-wrap gap-3">
            <Button type="button" variant="secondary" onClick={() => setImporting(true)} disabled={!identities.length}>
              <Upload />
              Import key
            </Button>
            <Button type="button" onClick={() => setGenerating(true)} disabled={!identities.length}>
              <Plus />
              New OpenPGP key
            </Button>
          </div>
        }
      />
      <QueryView query={keys} empty={null}>
        {(items) => (
          <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
            {items.length > 0 && (
              <form
                className="flex flex-col gap-3 sm:flex-row sm:items-end"
                onSubmit={async (event) => {
                  event.preventDefault()
                  if (!session) return
                  const passphrase = new FormData(event.currentTarget).get("passphrase")
                  if (typeof passphrase !== "string" || !passphrase) return
                  try {
                    await unlockKeys(session.user.id, passphrase, items.flatMap((key) => (key.privateKey && key.salt && key.iterations ? [{ ...key, protocol: key.protocol, privateKey: key.privateKey, salt: key.salt, iterations: key.iterations }] : [])))
                    setUnlocking("")
                    toast.success("Keys unlocked for this session")
                    event.currentTarget.reset()
                  } catch (error) {
                    setUnlocking(error instanceof Error ? error.message : "Those keys could not be unlocked.")
                  }
                }}
              >
                <Field id="encryption-passphrase" label={unlocked.length ? "Keys are unlocked" : "Unlock keys"} error={unlocking} hint={unlocked.length ? "They stay unlocked until you lock them or sign out." : "Enter the passphrase you chose when the keys were saved."}>
                  <Input id="encryption-passphrase" name="passphrase" type="password" autoComplete="current-password" />
                </Field>
                <div className="flex gap-3">
                  <Button type="submit" variant={unlocked.length ? "secondary" : "default"}>
                    {unlocked.length ? <LockOpen /> : <KeyRound />}
                    {unlocked.length ? "Unlock again" : "Unlock"}
                  </Button>
                  {unlocked.length > 0 && (
                    <Button type="button" variant="secondary" onClick={() => lockKeys()}>
                      <Lock />
                      Lock
                    </Button>
                  )}
                </div>
              </form>
            )}
            {items.length === 0 ? (
              <EmptyState icon={KeyRound} title="No keys yet" description="Import an S/MIME certificate or create an OpenPGP key for an address you send from." />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Address</TableHead>
                    <TableHead>Kind</TableHead>
                    <TableHead>Fingerprint</TableHead>
                    <TableHead className="w-24"><span className="sr-only">Actions</span></TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((key) => (
                    <TableRow key={key.id}>
                      <TableCell className="break-all">{key.address}</TableCell>
                      <TableCell>{key.protocol === "smime" ? "S/MIME" : "OpenPGP"}</TableCell>
                      <TableCell className="font-mono text-xs break-all">{formatFingerprint(key.fingerprint)}</TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1">
                          <IconButton
                            label={`Download the public ${key.protocol === "smime" ? "certificate" : "key"}`}
                            size="icon-sm"
                            onClick={() => download(`${key.address}.${key.protocol === "smime" ? "crt.pem" : "asc"}`, utf8(key.publicKey), "application/pem-certificate-chain")}
                          >
                            <Upload className="rotate-180" />
                          </IconButton>
                          <IconButton
                            label="Export the private key"
                            size="icon-sm"
                            disabled={!keyFor(key.address, key.protocol)}
                            onClick={() => {
                              const secret = keyFor(key.address, key.protocol)
                              if (!secret) return
                              const body = key.protocol === "openpgp" ? utf8(secret.privateKey) : utf8(pem("PRIVATE KEY", Uint8Array.from(atob(secret.privateKey), (c) => c.charCodeAt(0))))
                              download(`${key.address}.${key.protocol === "smime" ? "key.pem" : "asc"}`, body, "application/octet-stream")
                            }}
                          >
                            <LockOpen />
                          </IconButton>
                          <IconButton label={`Remove the key for ${key.address}`} size="icon-sm" onClick={() => setRemoving(key)}>
                            <Trash2 />
                          </IconButton>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        )}
      </QueryView>
      {importing && <ImportDialog identities={identities} keys={keys.data ?? []} userId={session?.user.id ?? ""} onClose={() => setImporting(false)} />}
      {generating && <GenerateDialog identities={identities} keys={keys.data ?? []} userId={session?.user.id ?? ""} displayName={session?.user.displayName ?? ""} onClose={() => setGenerating(false)} />}
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(open) => !open && setRemoving(null)}
        title="Remove this key?"
        description={removing ? `Mail already sent with ${removing.address} can still be verified by other people. You will not be able to decrypt new mail to this key unless you import it again.` : ""}
        confirmLabel="Remove key"
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing)}
      />
    </section>
  )
}

async function samePassphrase(passphrase: string, confirm: string, keys: EncryptionKey[]) {
  if (passphrase.length < 8) throw new Error("Use a passphrase of at least 8 characters.")
  if (keys.length === 0 && passphrase !== confirm) throw new Error("The passphrases do not match.")
  const existing = keys.find((key) => key.privateKey && key.salt && key.iterations)
  if (existing?.privateKey && existing.salt && existing.iterations) {
    await unwrapPrivateKey(Uint8Array.from(atob(existing.privateKey), (c) => c.charCodeAt(0)), Uint8Array.from(atob(existing.salt), (c) => c.charCodeAt(0)), existing.iterations, passphrase)
  }
  return passphrase
}

function ImportDialog({ identities, keys, userId, onClose }: { identities: Mailbox[]; keys: EncryptionKey[]; userId: string; onClose: () => void }) {
  const qc = useQueryClient()
  const [address, setAddress] = useState(identities[0]?.address ?? "")
  const [protocol, setProtocol] = useState<"smime" | "openpgp">("smime")
  const mailbox = identities.find((item) => item.address === address)
  const policy = useQuery({
    queryKey: ["encryption", mailbox?.orgId],
    enabled: !!mailbox,
    queryFn: () => api<EncryptionPolicy>(`/organizations/${mailbox!.orgId}/encryption`),
  })
  const save = useMutation({
    mutationFn: async (form: FormData) => {
      const passphrase = await samePassphrase(String(form.get("passphrase") ?? ""), String(form.get("confirm") ?? ""), keys)
      const file = form.get("file")
      const pasted = String(form.get("pasted") ?? "")
      const filePassword = String(form.get("filePassword") ?? "")
      let material: { privateKey: Uint8Array | string; certificate?: string; publicKey?: string; emails: string[] }
      if (protocol === "smime") {
        if (!(file instanceof File) || !file.size) throw new Error("Choose a PKCS#12 file or a PEM file.")
        const bytes = await fileBytes(file)
        const decoded = text(bytes)
        material = decoded.includes("BEGIN CERTIFICATE") ? await importSmimePem(decoded, decoded) : await importSmimePkcs12(bytes, filePassword)
      } else {
        const armored = pasted.trim() || (file instanceof File && file.size ? text(await fileBytes(file)) : "")
        if (!armored.includes("BEGIN PGP PRIVATE KEY")) throw new Error("Choose an OpenPGP private key.")
        const imported = await importOpenPGP(armored, filePassword)
        material = { privateKey: imported.privateKey, publicKey: imported.publicKey, emails: imported.emails }
      }
      if (!material.emails.some((email) => email === address.toLowerCase())) throw new Error(`This key is for ${material.emails.join(", ")}, not ${address}.`)
      const secret = typeof material.privateKey === "string" ? utf8(material.privateKey) : material.privateKey
      const wrapped = await wrapPrivateKey(secret, passphrase)
      const saved = await api<EncryptionKey>("/me/encryption-keys", {
        method: "PUT",
        body: {
          address,
          protocol,
          publicKey: material.certificate ?? material.publicKey,
          privateKey: bytesToBase64(wrapped.privateKey),
          salt: bytesToBase64(wrapped.salt),
          iterations: wrapped.iterations,
        },
      })
      if (userId) await unlockKeys(userId, passphrase, [saved, ...keys].flatMap((key) => (key.privateKey && key.salt && key.iterations ? [key as EncryptionKey & { privateKey: string; salt: string; iterations: number }] : [])))
    },
    onSuccess: () => {
      toast.success("Key saved")
      qc.invalidateQueries({ queryKey: ["encryption-keys"] })
      onClose()
    },
  })
  const allowed = protocol === "smime" ? policy.data?.smime !== "off" : policy.data?.openpgp !== "off"
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title="Import a key" description="The file password unlocks the key on this computer. The Aether passphrase encrypts it before upload and is not sent." submitLabel="Save key" pending={save.isPending} onSubmit={(event) => { event.preventDefault(); save.mutate(new FormData(event.currentTarget)) }}>
      <Field id="import-address" label="Address">
        <Select value={address} onValueChange={setAddress}>
          <SelectTrigger id="import-address"><SelectValue /></SelectTrigger>
          <SelectContent>{identities.map((item) => <SelectItem key={item.id} value={item.address}>{item.address}</SelectItem>)}</SelectContent>
        </Select>
      </Field>
      <Field id="import-protocol" label="Kind" error={policy.data && !allowed ? "Your organization has turned this off." : undefined}>
        <Select value={protocol} onValueChange={(value) => setProtocol(value as "smime" | "openpgp")}>
          <SelectTrigger id="import-protocol"><SelectValue /></SelectTrigger>
          <SelectContent>{protocols.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent>
        </Select>
      </Field>
      <Field id="import-file" label={protocol === "smime" ? "Certificate and private key" : "Private key"} hint={protocol === "smime" ? "A .p12 or .pfx file, or one PEM containing the certificate and the key." : "An ASCII-armored private key."}>
        <Input id="import-file" name="file" type="file" accept={protocol === "smime" ? ".p12,.pfx,.pem,.crt,.key" : ".asc,.pgp,.key,.txt"} />
      </Field>
      {protocol === "openpgp" && (
        <Field id="import-pasted" label="Or paste the key">
          <Textarea id="import-pasted" name="pasted" spellCheck={false} className="font-mono text-xs" />
        </Field>
      )}
      <Field id="import-file-password" label="File password" hint="The password for the PKCS#12 file or the existing OpenPGP key. Leave it blank if there is none.">
        <Input id="import-file-password" name="filePassword" type="password" autoComplete="off" />
      </Field>
      <PassphraseFields confirm={keys.length === 0} />
    </FormDialog>
  )
}

function GenerateDialog({ identities, keys, userId, displayName, onClose }: { identities: Mailbox[]; keys: EncryptionKey[]; userId: string; displayName: string; onClose: () => void }) {
  const qc = useQueryClient()
  const [address, setAddress] = useState(identities[0]?.address ?? "")
  const save = useMutation({
    mutationFn: async (form: FormData) => {
      const passphrase = await samePassphrase(String(form.get("passphrase") ?? ""), String(form.get("confirm") ?? ""), keys)
      const created = await generateOpenPGP(displayName || address, address)
      const wrapped = await wrapPrivateKey(utf8(created.privateKey), passphrase)
      const saved = await api<EncryptionKey>("/me/encryption-keys", {
        method: "PUT",
        body: { address, protocol: "openpgp", publicKey: created.publicKey, privateKey: bytesToBase64(wrapped.privateKey), salt: bytesToBase64(wrapped.salt), iterations: wrapped.iterations },
      })
      if (userId) await unlockKeys(userId, passphrase, [saved, ...keys].flatMap((key) => (key.privateKey && key.salt && key.iterations ? [key as EncryptionKey & { privateKey: string; salt: string; iterations: number }] : [])))
    },
    onSuccess: () => {
      toast.success("OpenPGP key created")
      qc.invalidateQueries({ queryKey: ["encryption-keys"] })
      onClose()
    },
  })
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title="New OpenPGP key" description="Aether creates the key in this browser. The private key is encrypted with your passphrase before it is stored." submitLabel="Create key" pending={save.isPending} onSubmit={(event) => { event.preventDefault(); save.mutate(new FormData(event.currentTarget)) }}>
      <Field id="generate-address" label="Address">
        <Select value={address} onValueChange={setAddress}>
          <SelectTrigger id="generate-address"><SelectValue /></SelectTrigger>
          <SelectContent>{identities.map((item) => <SelectItem key={item.id} value={item.address}>{item.address}</SelectItem>)}</SelectContent>
        </Select>
      </Field>
      <PassphraseFields confirm={keys.length === 0} />
    </FormDialog>
  )
}

function PassphraseFields({ confirm }: { confirm: boolean }) {
  return (
    <>
      <Field id="key-passphrase" label="Aether passphrase" hint="This encrypts the private key. It is not saved and cannot be recovered.">
        <Input id="key-passphrase" name="passphrase" type="password" autoComplete="new-password" required minLength={8} />
      </Field>
      {confirm && (
        <Field id="key-passphrase-confirm" label="Confirm passphrase">
          <Input id="key-passphrase-confirm" name="confirm" type="password" autoComplete="new-password" required minLength={8} />
        </Field>
      )}
    </>
  )
}

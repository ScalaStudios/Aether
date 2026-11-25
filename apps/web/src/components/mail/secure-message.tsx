"use client"

import { useMutation, useQuery } from "@tanstack/react-query"
import { Download, ShieldCheck, ShieldQuestion } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import { readAutocrypt } from "@/lib/crypto/openpgp"
import { keyFor } from "@/lib/crypto/session"
import { openRaw, type OpenedMail } from "@/lib/crypto/seal"
import { formatBytes } from "@/lib/format"
import { messageDocument } from "@/lib/mail"
import { myMailboxesQuery } from "@/lib/queries"
import type { Message, RecipientKey } from "@/lib/types"

export function SecureMessage({ message, mailboxId, folder }: { message: Message; mailboxId: string; folder: string }) {
  const mailboxes = useQuery(myMailboxesQuery)
  const address = mailboxes.data?.find((item) => item.id === mailboxId)?.address ?? ""
  const [opened, setOpened] = useState<OpenedMail | null>(null)
  const sender = message.from[0]?.address ?? ""
  const open = useMutation({
    mutationFn: async () => {
      const blob = await api<Blob>(`/mailboxes/${mailboxId}/messages/${message.uid}/source`, { query: { folder }, blob: true })
      const raw = new Uint8Array(await blob.arrayBuffer())
      const found = sender ? await api<RecipientKey[]>("/me/recipient-keys", { query: { address: sender } }).catch(() => []) : []
      const autocrypt = message.protection?.autocrypt ? await readAutocrypt(message.protection.autocrypt) : null
      const result = await openRaw(raw, {
        smimeKey: address ? keyFor(address, "smime")?.privateKey : undefined,
        openpgpKey: address ? keyFor(address, "openpgp")?.privateKey : undefined,
        smimeCerts: found.filter((key) => key.protocol === "smime").map((key) => key.publicKey),
        openpgpKeys: [...found.filter((key) => key.protocol === "openpgp").map((key) => key.publicKey), autocrypt?.publicKey].filter((key): key is string => !!key),
      })
      if (result.verified && result.peerKey && sender) {
        await api("/me/recipient-keys", { method: "POST", body: { address: sender, protocol: "smime", publicKey: result.peerKey, source: "message" } }).catch(() => undefined)
      }
      return result
    },
    onSuccess: setOpened,
  })
  const saveAutocrypt = useMutation({
    mutationFn: async () => {
      const header = message.protection?.autocrypt
      if (!header || !sender) throw new Error("This message has no OpenPGP key.")
      const key = await readAutocrypt(header)
      if (!key || key.address !== sender.toLowerCase()) throw new Error("The key in this message is not for the sender.")
      await api("/me/recipient-keys", { method: "POST", body: { address: sender, protocol: "openpgp", publicKey: key.publicKey, source: "autocrypt" } })
    },
    onSuccess: () => toast.success("Key saved for this sender"),
  })
  const body = opened ?? { text: message.protection?.encrypted ? "" : message.text, html: message.protection?.encrypted ? "" : message.html, attachments: [] as OpenedMail["attachments"] }
  const locked = !!message.protection?.encrypted && !opened
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 sm:flex-row sm:items-center">
        {opened?.verified ? <ShieldCheck aria-hidden className="size-5 text-success" /> : <ShieldQuestion aria-hidden className="size-5 text-ember" />}
        <p className="min-w-0 flex-1 text-sm text-fg-secondary">
          {opened
            ? opened.verified
              ? `Verified ${opened.protocol === "smime" ? "S/MIME" : "OpenPGP"} signature${opened.encrypted ? " and decrypted" : ""}.`
              : opened.encrypted
                ? "Decrypted. The signature could not be checked."
                : "The signature could not be verified."
            : message.protection?.encrypted
              ? "This message is encrypted. Unlock your key in Settings, then decrypt it here."
              : "This message is signed. Check the signature before trusting it."}
        </p>
        <div className="flex flex-wrap gap-2">
          {message.protection?.autocrypt && (
            <Button type="button" variant="secondary" size="sm" loading={saveAutocrypt.isPending} onClick={() => saveAutocrypt.mutate()}>
              Save their key
            </Button>
          )}
          <Button type="button" size="sm" loading={open.isPending} onClick={() => open.mutate()}>
            {message.protection?.encrypted ? "Decrypt" : "Verify"}
          </Button>
        </div>
      </div>
      {!locked && body.attachments.length > 0 && (
        <ul aria-label="Decrypted attachments" className="flex flex-wrap gap-2">
          {body.attachments.map((file) => (
            <li key={file.filename}>
              <button
                type="button"
                className="flex min-h-10 max-w-[280px] items-center gap-2 rounded-lg border border-strong px-3 py-2 text-sm transition-colors duration-160 hover:bg-muted"
                onClick={() => {
                  const url = URL.createObjectURL(new Blob([Uint8Array.from(file.data)], { type: file.contentType }))
                  const link = document.createElement("a")
                  link.href = url
                  link.download = file.filename
                  link.click()
                  URL.revokeObjectURL(url)
                }}
              >
                <Download aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
                <span className="truncate">{file.filename}</span>
                <span className="shrink-0 text-xs text-fg-tertiary">{formatBytes(file.data.byteLength)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {!locked && (body.html ? (
        <iframe title="Message body" sandbox="allow-popups allow-popups-to-escape-sandbox" srcDoc={messageDocument(body.html)} className="min-h-[400px] w-full flex-1 rounded-xl border border-border bg-white" />
      ) : (
        <pre className="font-sans text-base whitespace-pre-wrap break-words">{body.text}</pre>
      ))}
    </div>
  )
}

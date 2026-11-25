"use client"

import { useMutation } from "@tanstack/react-query"
import Image from "next/image"
import { useSearchParams } from "next/navigation"
import { useState, useSyncExternalStore } from "react"
import { toast } from "sonner"

import { BrandHeading } from "@/components/branding"
import { Field } from "@/components/field"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { useHydrated } from "@/hooks/use-hydrated"
import { api } from "@/lib/api"
import { openProtected, openReply, sealReply, type ProtectedPayload, type ProtectedReply } from "@/lib/crypto/protected"
import { formatDateTime } from "@/lib/format"
import { messageDocument } from "@/lib/mail"
import type { ProtectedMessage } from "@/lib/types"

export function ProtectedMessageView() {
  const params = useSearchParams()
  const mailbox = params.get("mailbox") ?? ""
  const message = params.get("message") ?? ""
  const client = useHydrated()
  const link = useSyncExternalStore(subscribeHash, readHash, () => null)
  const senderView = !link && !!mailbox && !!message
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-12 px-5 py-12">
      <div className="flex w-full max-w-[560px] flex-col gap-8">
        <BrandHeading />
        {!client ? null : link ? (
          <Recipient link={link} />
        ) : senderView ? (
          <Sender mailbox={mailbox} message={message} />
        ) : (
          <section className="flex flex-col gap-3 rounded-xl border border-border bg-card p-6">
            <h1 className="text-h3">This link is incomplete</h1>
            <p className="text-fg-secondary">Open the link from the email. The password comes from the sender, not from the link.</p>
          </section>
        )}
      </div>
      <p className="flex items-center gap-2 text-sm text-fg-tertiary">
        <Image src="/brand/scala-symbol-gradient.svg" alt="" width={24} height={31} className="h-[31px] w-6 light:hidden" />
        <Image src="/brand/scala-symbol-ink.svg" alt="" width={24} height={31} className="hidden h-[31px] w-6 light:block" />
        A Scala Studios project
      </p>
    </main>
  )
}

function subscribeHash(onChange: () => void) {
  window.addEventListener("hashchange", onChange)
  return () => window.removeEventListener("hashchange", onChange)
}

let cachedHash = ""
let cachedLink: { id: string; access: string } | null = null

function readHash() {
  const hash = window.location.hash
  if (hash === cachedHash) return cachedLink
  cachedHash = hash
  const match = /^([0-9a-f-]{36})\.([0-9a-f]{64})$/i.exec(hash.replace(/^#/, ""))
  cachedLink = match ? { id: match[1], access: match[2] } : null
  return cachedLink
}

function Recipient({ link }: { link: { id: string; access: string } }) {
  const [password, setPassword] = useState("")
  const [item, setItem] = useState<ProtectedMessage | null>(null)
  const [payload, setPayload] = useState<ProtectedPayload | null>(null)
  const [reply, setReply] = useState("")
  const [sent, setSent] = useState(false)
  const open = useMutation({
    mutationFn: async () => {
      const loaded = await api<ProtectedMessage>(`/protected/${link.id}/open`, { method: "POST", body: { access: link.access } })
      const clear = await openProtected(password, link.id, loaded.salt, loaded.message)
      return { loaded, clear }
    },
    onSuccess: ({ loaded, clear }) => {
      setItem(loaded)
      setPayload(clear)
    },
  })
  const send = useMutation({
    mutationFn: async () => {
      if (!payload || !item) return
      const sealed = await sealReply(password, link.id, item.salt, { text: reply, html: "" })
      await api(`/protected/${link.id}/reply`, { method: "POST", body: { access: link.access, proof: payload.replyProof, reply: sealed } })
    },
    onSuccess: () => {
      setSent(true)
      toast.success("Reply sent")
    },
  })
  if (!payload || !item) {
    return (
      <form className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6" onSubmit={(event) => { event.preventDefault(); open.mutate() }}>
        <div className="flex flex-col gap-2">
          <h1 className="text-h3">Open protected message</h1>
          <p className="text-fg-secondary">Enter the password the sender gave you. It never leaves this browser.</p>
        </div>
        <Field id="protected-password" label="Password">
          <Input id="protected-password" type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} />
        </Field>
        <Button type="submit" loading={open.isPending} disabled={!password}>Open</Button>
      </form>
    )
  }
  return (
    <article className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <header className="flex flex-col gap-1">
        <p className="text-sm text-fg-tertiary">From {item.sender}</p>
        <h1 className="text-h3 break-words">{payload.subject || "(no subject)"}</h1>
        <p className="text-sm text-fg-tertiary">Expires {formatDateTime(item.expiresAt)}</p>
      </header>
      <Body text={payload.text} html={payload.html} />
      {sent || item.replied ? (
        <p className="text-sm text-fg-secondary">Your reply has been sent. This link accepts only one.</p>
      ) : (
        <form className="flex flex-col gap-3 border-t border-border pt-4" onSubmit={(event) => { event.preventDefault(); send.mutate() }}>
          <Field id="protected-reply" label="Reply once" hint="The reply is encrypted with the same password.">
            <Textarea id="protected-reply" value={reply} onChange={(event) => setReply(event.target.value)} />
          </Field>
          <Button type="submit" loading={send.isPending} disabled={!reply.trim()}>Send reply</Button>
        </form>
      )}
    </article>
  )
}

function Sender({ mailbox, message }: { mailbox: string; message: string }) {
  const [password, setPassword] = useState("")
  const [item, setItem] = useState<ProtectedMessage | null>(null)
  const [payload, setPayload] = useState<ProtectedPayload | null>(null)
  const [reply, setReply] = useState<ProtectedReply | null>(null)
  const open = useMutation({
    mutationFn: async () => {
      const loaded = await api<ProtectedMessage>(`/mailboxes/${mailbox}/protected/${message}`)
      const clear = await openProtected(password, message, loaded.salt, loaded.message)
      const answer = loaded.reply ? await openReply(password, message, loaded.salt, loaded.reply) : null
      return { loaded, clear, answer }
    },
    onSuccess: ({ loaded, clear, answer }) => {
      setItem(loaded)
      setPayload(clear)
      setReply(answer)
    },
  })
  if (!payload || !item) {
    return (
      <form className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6" onSubmit={(event) => { event.preventDefault(); open.mutate() }}>
        <div className="flex flex-col gap-2">
          <h1 className="text-h3">Your protected message</h1>
          <p className="text-fg-secondary">Enter the password you shared. A reply appears here once the recipient sends one.</p>
        </div>
        <Field id="sender-password" label="Password">
          <Input id="sender-password" type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} />
        </Field>
        <Button type="submit" loading={open.isPending} disabled={!password}>Open</Button>
      </form>
    )
  }
  return (
    <article className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <header className="flex flex-col gap-1">
        <p className="text-sm text-fg-tertiary">To {item.recipient}</p>
        <h1 className="text-h3 break-words">{payload.subject || "(no subject)"}</h1>
        <p className="text-sm text-fg-tertiary">Expires {formatDateTime(item.expiresAt)}</p>
      </header>
      <Body text={payload.text} html={payload.html} />
      {reply ? (
        <section className="flex flex-col gap-2 border-t border-border pt-4">
          <h2 className="font-semibold">Reply</h2>
          <Body text={reply.text} html={reply.html} />
        </section>
      ) : (
        <p className="text-sm text-fg-secondary">No reply yet.</p>
      )}
    </article>
  )
}

function Body({ text, html }: { text: string; html: string }) {
  if (html.trim()) {
    return <iframe title="Protected message" sandbox="" srcDoc={messageDocument(html)} className="min-h-48 w-full rounded-lg border border-border bg-white" />
  }
  return <pre className="font-sans text-base whitespace-pre-wrap break-words">{text}</pre>
}

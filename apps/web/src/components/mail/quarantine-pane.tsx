"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, ChevronRight, EyeOff, Paperclip, ShieldAlert, ShieldCheck, Trash2 } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { IconButton } from "@/components/icon-button"
import { AddressLine } from "@/components/mail/reading-pane"
import { EmptyState, ErrorState } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { api } from "@/lib/api"
import { formatAddress, formatBytes, formatDate, formatDateTime } from "@/lib/format"
import { messageDocument } from "@/lib/mail"
import { quarantineCategories, risky, virusName } from "@/lib/quarantine"
import type { QuarantinePreview } from "@/lib/types"

export function QuarantineMessage({ item, showMailbox }: { item: QuarantinePreview; showMailbox?: boolean }) {
  const m = item.message
  const category = quarantineCategories[item.category]
  const virus = virusName(item.symbols)
  const signals = item.symbols.filter((s) => s.score > 0)
  const facts = [
    item.score > 0 && `Spam score ${item.score.toFixed(1)}`,
    item.status === "released" && item.releasedAt
      ? `Released ${formatDate(item.releasedAt)}${item.releasedBy ? ` by ${item.releasedBy}` : ""}`
      : `Kept until ${formatDate(item.expiresAt)}`,
    item.status === "requested" && item.requestedAt && `Release requested ${formatDate(item.requestedAt)}`,
  ].filter(Boolean)

  return (
    <article key={item.id} className="animate-content flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-6">
      <header className="flex flex-col gap-3">
        <h2 className="text-h3 break-words">{m.subject || "(no subject)"}</h2>
        <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
          <p className="font-semibold break-all">{m.from.map(formatAddress).join(", ") || "Unknown sender"}</p>
          <time dateTime={item.receivedAt} className="text-sm text-fg-tertiary">
            {formatDateTime(item.receivedAt)}
          </time>
        </div>
        <dl className="flex flex-col gap-1">
          <AddressLine label="To" addresses={m.to} />
          <AddressLine label="Cc" addresses={m.cc} />
          {showMailbox && <AddressLine label="For" addresses={[{ address: item.mailbox }]} />}
        </dl>
      </header>
      <section aria-label="Why this message was held" className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4">
        <div className="flex items-start gap-3">
          <ShieldAlert aria-hidden className={cn("mt-0.5 size-5 shrink-0", category.tone === "danger" ? "text-danger" : "text-warning")} />
          <div className="flex min-w-0 flex-col gap-1">
            <p className="font-semibold">Held as {category.label.toLowerCase()}</p>
            <p className="text-sm text-fg-secondary">{virus ? `The virus scanner found ${virus}.` : category.reason}</p>
            <p className="text-xs text-fg-tertiary tabular-nums">{facts.join(" · ")}</p>
          </div>
        </div>
        {signals.length > 0 && (
          <details className="group">
            <summary className="flex min-h-8 cursor-pointer list-none items-center gap-1 text-sm font-medium text-fg-secondary transition-colors duration-160 hover:text-foreground [&::-webkit-details-marker]:hidden">
              <ChevronRight aria-hidden className="size-4 transition-transform duration-160 group-open:rotate-90" />
              Spam filter signals
            </summary>
            <ul className="mt-2 flex flex-col gap-1 pl-5 font-mono text-xs">
              {signals.map((s) => (
                <li key={s.name} className="flex gap-3">
                  <span className="w-12 shrink-0 text-right text-fg-tertiary tabular-nums">+{s.score.toFixed(2)}</span>
                  <span className="min-w-0 break-all">
                    {s.name}
                    {s.options?.length ? <span className="text-fg-tertiary"> {s.options.join(", ")}</span> : null}
                  </span>
                </li>
              ))}
            </ul>
          </details>
        )}
      </section>
      <p className="flex items-center gap-2 text-sm text-fg-tertiary">
        <EyeOff aria-hidden className="size-4 shrink-0" />
        Links, remote images and attachments are turned off in this preview.
      </p>
      {m.attachments.length > 0 && (
        <ul aria-label="Attachments" className="flex flex-wrap gap-2">
          {m.attachments.map((a) => (
            <li key={a.part} className="flex min-h-10 max-w-[280px] items-center gap-2 rounded-lg border border-border px-3 py-2 text-sm text-fg-secondary">
              <Paperclip aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
              <span className="truncate">{a.filename || "Attachment"}</span>
              <span className="shrink-0 text-xs text-fg-tertiary">{formatBytes(a.size)}</span>
            </li>
          ))}
        </ul>
      )}
      {m.html ? (
        <iframe title="Message preview" sandbox="" srcDoc={messageDocument(m.html)} className="min-h-[400px] w-full flex-1 rounded-xl border border-border bg-white" />
      ) : (
        <pre className="font-sans text-base whitespace-pre-wrap break-words">{m.text || "This message has no text."}</pre>
      )}
    </article>
  )
}

export function PreviewSkeleton() {
  return (
    <div aria-busy aria-label="Loading message" className="flex flex-col gap-4 p-6">
      <Skeleton className="h-7 w-2/3" />
      <Skeleton className="h-4 w-1/2" />
      <Skeleton className="h-20 w-full" />
      <Skeleton className="mt-4 h-64 w-full" />
    </div>
  )
}

type Action = "release" | "allow-sender" | "request" | "delete"

export function QuarantinePane({
  className,
  mailboxId,
  itemId,
  readOnly,
  onBack,
  onDone,
}: {
  className?: string
  mailboxId: string
  itemId: string | null
  readOnly?: boolean
  onBack: () => void
  onDone: (id: string) => void
}) {
  const qc = useQueryClient()
  const [confirm, setConfirm] = useState<Action | null>(null)
  const preview = useQuery({
    queryKey: ["quarantine-item", mailboxId, itemId],
    queryFn: () => api<QuarantinePreview>(`/mailboxes/${mailboxId}/quarantine/${itemId}`),
    enabled: itemId !== null,
  })
  const item = preview.data

  const act = useMutation({
    mutationFn: (action: Action) =>
      action === "delete"
        ? api(`/mailboxes/${mailboxId}/quarantine/${itemId}`, { method: "DELETE" })
        : api(`/mailboxes/${mailboxId}/quarantine/${itemId}/${action}`, { method: "POST" }),
    onSuccess: (_d, action) => {
      toast.success(
        {
          release: "Released to your inbox",
          "allow-sender": `Released. Mail from ${item?.from.address} won’t be held as spam again.`,
          request: "Release requested. An administrator will review it.",
          delete: "Deleted from quarantine",
        }[action]
      )
      setConfirm(null)
      qc.invalidateQueries({ queryKey: ["quarantine", mailboxId] })
      qc.invalidateQueries({ queryKey: ["quarantine-item", mailboxId, itemId] })
      if (action === "request" || !itemId) return
      if (action !== "delete") {
        qc.invalidateQueries({ queryKey: ["folders", mailboxId] })
        qc.invalidateQueries({ queryKey: ["messages", mailboxId] })
      }
      if (action === "allow-sender") {
        qc.invalidateQueries({ queryKey: ["allowed-senders", mailboxId] })
        qc.invalidateQueries({ queryKey: ["sender-lists", "mailbox", mailboxId] })
      }
      onDone(itemId)
    },
  })

  if (itemId === null) {
    return (
      <section aria-label="Quarantine preview" className={cn("items-center justify-center", className)}>
        <EmptyState icon={ShieldCheck} title="No message selected" description="Choose a held message to preview it. Links and remote images stay off, so opening it here is safe." />
      </section>
    )
  }

  const label = item ? quarantineCategories[item.category].label.toLowerCase() : ""
  const pending = (a: Action) => act.isPending && act.variables === a
  const release = () => (item && risky(item.category) ? setConfirm("release") : act.mutate("release"))

  return (
    <section aria-label="Quarantine preview" className={cn("min-h-0 flex-col", className)}>
      <div className="flex min-h-14 shrink-0 items-center gap-2 overflow-x-auto border-b border-border px-4 py-2">
        <IconButton label="Back to quarantine" className="md:hidden" onClick={onBack}>
          <ArrowLeft />
        </IconButton>
        {item && !readOnly && (
          <>
            {item.access === "release" && (
              <>
                <Button size="sm" loading={pending("release")} disabled={act.isPending} onClick={release}>
                  Release to Inbox
                </Button>
                {item.canAllowSender && (
                  <Button size="sm" variant="secondary" loading={pending("allow-sender")} disabled={act.isPending} onClick={() => act.mutate("allow-sender")}>
                    Release and allow sender
                  </Button>
                )}
              </>
            )}
            {item.access === "request" &&
              (item.status === "requested" ? (
                <StatusBadge tone="info">Release requested</StatusBadge>
              ) : (
                <Button size="sm" loading={pending("request")} disabled={act.isPending} onClick={() => act.mutate("request")}>
                  Request release
                </Button>
              ))}
            <IconButton label="Delete" className="ml-auto" disabled={act.isPending} onClick={() => setConfirm("delete")}>
              <Trash2 />
            </IconButton>
          </>
        )}
      </div>
      {preview.isPending ? <PreviewSkeleton /> : preview.isError ? <ErrorState error={preview.error} onRetry={() => preview.refetch()} /> : item && <QuarantineMessage item={item} />}
      <ConfirmDialog
        open={confirm === "delete"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title="Delete this message?"
        description="It is removed from quarantine for good. The sender is not told."
        confirmLabel="Delete message"
        pending={pending("delete")}
        onConfirm={() => act.mutate("delete")}
      />
      <ConfirmDialog
        open={confirm === "release"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={`Release this ${label} message?`}
        description={
          item?.category === "malware"
            ? `It contains ${virusName(item.symbols) ?? "malware"}. Opening its attachments can infect your device.`
            : "Phishing tries to get you to enter a password or pay. Release it only if you trust the sender and every link in it."
        }
        confirmLabel="Release anyway"
        pending={pending("release")}
        onConfirm={() => act.mutate("release")}
      />
    </section>
  )
}

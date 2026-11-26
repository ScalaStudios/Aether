"use client"

import { useQuery } from "@tanstack/react-query"
import { Ban, CircleCheck, Clock, Forward, Inbox, Mail, OctagonX, PauseCircle, PlayCircle, Send, ShieldAlert, ShieldCheck, Trash2, Undo2, type LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"

import { arrival, formatTLS, traceStatuses } from "@/components/admin/message-trace/labels"
import { CopyButton } from "@/components/copy-button"
import { QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge, tones, type Tone } from "@/components/status-badge"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { api } from "@/lib/api"
import { formatBytes, formatDateTime, formatPreciseDateTime } from "@/lib/format"
import type { MessageTraceDetail, MessageTraceEvent, MessageTraceStatus, MessageTraceSummary } from "@/lib/types"

const statusHelp: Record<MessageTraceStatus, string> = {
  delivered: "Every recipient you can see received it.",
  junk: "The spam filter marked it as spam, so it was filed in the recipient’s Junk folder.",
  queued: "Accepted and waiting for its first delivery attempt.",
  deferred: "Delivery failed temporarily. The server retries for up to five days before returning it to the sender.",
  held: "Held in the queue. It is delivered only after an administrator releases it.",
  bounced: "At least one recipient could not be reached. The sender received a non-delivery report.",
  rejected: "Aether refused the message or a recipient during the SMTP conversation. The sending server received the reason below.",
  discarded: "Accepted and then dropped without delivery or a report.",
  deleted: "Removed from the queue by an administrator before it was delivered.",
  unknown: "The record has no delivery entries, usually because it was received before message trace started.",
}

function describe(e: MessageTraceEvent, spam: boolean): { title: string; tone: Tone; icon: LucideIcon } {
  switch (e.kind) {
    case "delivered":
      return spam ? { title: `Delivered to Junk for ${e.recipient}`, tone: "warning", icon: ShieldAlert } : { title: `Delivered to ${e.recipient}`, tone: "success", icon: CircleCheck }
    case "relayed":
      return { title: `Accepted by the server for ${e.recipient}`, tone: "success", icon: Send }
    case "forwarded":
      return { title: `Forwarded to ${e.recipient}`, tone: "success", icon: Forward }
    case "deferred":
      return { title: `Delivery to ${e.recipient} delayed`, tone: "warning", icon: Clock }
    case "bounced":
      return { title: `Could not deliver to ${e.recipient}`, tone: "danger", icon: OctagonX }
    case "expired":
      return { title: "Retries ran out, returned to the sender", tone: "danger", icon: OctagonX }
    case "rejected":
      if (e.recipient) return { title: `Refused recipient ${e.recipient}`, tone: "danger", icon: Ban }
      return e.dsn.startsWith("4")
        ? { title: "Refused for now, the sending server should retry", tone: "warning", icon: Clock }
        : { title: "Refused", tone: "danger", icon: Ban }
    case "discarded":
      return { title: "Discarded", tone: "neutral", icon: Trash2 }
    case "held":
      return { title: "Held in the queue", tone: "warning", icon: PauseCircle }
    case "released":
      return { title: "Released from hold", tone: "info", icon: PlayCircle }
    case "deleted":
      return { title: "Deleted from the queue", tone: "neutral", icon: Trash2 }
    case "notified":
      return { title: "Non-delivery report sent to the sender", tone: "neutral", icon: Undo2 }
  }
}

function Step({ icon: Icon, tone, title, at, children }: { icon: LucideIcon; tone: Tone; title: string; at: string; children?: React.ReactNode }) {
  return (
    <li className="group relative flex gap-4 pb-6 last:pb-0">
      <span aria-hidden className="absolute top-9 bottom-1 left-4 w-px bg-border group-last:hidden" />
      <span className={cn("grid size-8 shrink-0 place-items-center rounded-full", tones[tone].className)}>
        <Icon aria-hidden className="size-4" />
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-2 pt-1">
        <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
          <p className="min-w-0 font-semibold break-words">{title}</p>
          <time dateTime={at} className="text-sm whitespace-nowrap text-fg-tertiary tabular-nums">
            {formatPreciseDateTime(at)}
          </time>
        </div>
        {children}
      </div>
    </li>
  )
}

function Reply({ children }: { children: React.ReactNode }) {
  return <p className="rounded-lg bg-sunken px-3 py-2 font-mono text-sm break-words text-fg-secondary">{children}</p>
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1 border-b border-border p-3 last:border-b-0 sm:flex-row sm:gap-4">
      <dt className="w-28 shrink-0 pt-1 text-label text-fg-tertiary">{label}</dt>
      <dd className="flex min-w-0 flex-1 flex-col gap-1">{children}</dd>
    </div>
  )
}

function Copyable({ value, label }: { value: string; label: string }) {
  return (
    <span className="flex items-start gap-2">
      <span className="min-w-0 flex-1 pt-1 font-mono text-sm break-all">{value}</span>
      <CopyButton value={value} label={label} />
    </span>
  )
}

function client(m: MessageTraceDetail) {
  const name = m.clientName && m.clientName !== "unknown" ? m.clientName : ""
  if (!m.clientIp) return name
  return name ? `${name} [${m.clientIp}]` : m.clientIp
}

function Trace({ m, onSelect }: { m: MessageTraceDetail; onSelect: (id: number) => void }) {
  const status = traceStatuses[m.status]
  const recipients = m.recipients.length > 0 ? m.recipients : Object.keys(m.recipientStatus)
  return (
    <>
      <div className="flex flex-col gap-2">
        <StatusBadge tone={status.tone}>{status.label}</StatusBadge>
        <p className="text-fg-secondary">{statusHelp[m.status]}</p>
      </div>

      <section aria-labelledby="trace-overview" className="flex flex-col gap-3">
        <h3 id="trace-overview" className="text-label text-fg-secondary">
          Overview
        </h3>
        <dl className="flex flex-col rounded-xl border border-border">
          <Row label="From">
            <span className="pt-1 font-mono text-sm break-all">{m.sender || <span className="font-sans text-fg-tertiary">No sender (a delivery report)</span>}</span>
          </Row>
          <Row label={recipients.length === 1 ? "To" : `To (${recipients.length})`}>
            <ul className="flex flex-col gap-2 pt-1">
              {recipients.map((r) => {
                const s = m.recipientStatus[r]
                return (
                  <li key={r} className="flex flex-wrap items-center justify-between gap-2">
                    <span className="min-w-0 font-mono text-sm break-all">{r}</span>
                    {s && recipients.length > 1 && <StatusBadge tone={traceStatuses[s].tone}>{traceStatuses[s].label}</StatusBadge>}
                  </li>
                )
              })}
              {recipients.length === 0 && <li className="text-sm text-fg-tertiary">Not recorded yet</li>}
            </ul>
          </Row>
          <Row label="Arrived">
            <span className="pt-1">{arrival(m.service)}</span>
            {client(m) && <span className="font-mono text-sm break-all text-fg-secondary">{client(m)}</span>}
            {m.saslUsername && m.service !== "sieve-relay" && <span className="text-sm text-fg-secondary">Signed in as {m.saslUsername}</span>}
          </Row>
          <Row label="Connection">
            <span className="pt-1">{m.tls === null ? <span className="text-fg-tertiary">Not recorded</span> : m.tls ? `Encrypted, ${formatTLS(m.tls)}` : "Not encrypted"}</span>
          </Row>
          {m.spamScore !== null && (
            <Row label="Spam filter">
              <span className="pt-1">
                Score <span className="tabular-nums">{m.spamScore.toFixed(2)}</span>, {m.spam ? "marked as spam" : "not spam"}
              </span>
            </Row>
          )}
          {m.size !== null && (
            <Row label="Size">
              <span className="pt-1 tabular-nums">{formatBytes(m.size)}</span>
            </Row>
          )}
          {m.messageId && (
            <Row label="Message-ID">
              <Copyable value={m.messageId} label="Copy Message-ID" />
            </Row>
          )}
          {m.queueId && (
            <Row label="Queue ID">
              <Copyable value={m.queueId} label="Copy queue ID" />
            </Row>
          )}
        </dl>
      </section>

      <section aria-labelledby="trace-path" className="flex flex-col gap-4">
        <h3 id="trace-path" className="text-label text-fg-secondary">
          Path
        </h3>
        <ol className="flex flex-col">
          <Step icon={Inbox} tone="info" title="Received" at={m.receivedAt}>
            <p className="text-sm text-fg-secondary">
              {arrival(m.service)}
              {client(m) && `, ${client(m)}`}
            </p>
          </Step>
          {m.spamScore !== null && (
            <Step icon={m.spam ? ShieldAlert : ShieldCheck} tone={m.spam ? "warning" : "success"} title={m.spam ? "Marked as spam" : "Passed the spam filter"} at={m.receivedAt}>
              <p className="text-sm text-fg-secondary">
                Rspamd score <span className="tabular-nums">{m.spamScore.toFixed(2)}</span>
              </p>
            </Step>
          )}
          {m.events.map((e, i) => {
            const d = describe(e, m.spam)
            return (
              <Step key={i} icon={d.icon} tone={d.tone} title={d.title} at={e.at}>
                {e.origRecipient && <p className="text-sm text-fg-secondary">Addressed to {e.origRecipient}</p>}
                {e.relay && e.relay !== "none" && (
                  <p className="text-sm break-all text-fg-secondary">
                    Server <span className="font-mono">{e.relay}</span>
                    {e.tls && `, ${formatTLS(e.tls)}`}
                  </p>
                )}
                {e.kind === "notified" ? (
                  <p className="text-sm text-fg-secondary">
                    Report queue ID <span className="font-mono">{e.detail}</span>
                  </p>
                ) : (
                  e.detail && <Reply>{e.detail}</Reply>
                )}
              </Step>
            )
          })}
        </ol>
      </section>

      {m.related.length > 0 && (
        <section aria-labelledby="trace-related" className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <h3 id="trace-related" className="text-label text-fg-secondary">
              Same Message-ID
            </h3>
            <p className="text-sm text-fg-tertiary">Other copies of this message, such as one a mail rule forwarded.</p>
          </div>
          <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
            {m.related.map((r) => (
              <li key={r.id}>
                <button
                  type="button"
                  onClick={() => onSelect(r.id)}
                  className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-left transition-colors duration-160 hover:bg-muted/50 max-md:min-h-11"
                >
                  <Mail aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span>{arrival(r.service)}</span>
                    <span className="truncate text-sm text-fg-tertiary">
                      {formatDateTime(r.receivedAt)}
                      {r.sender && ` · ${r.sender}`}
                    </span>
                  </span>
                  <StatusBadge tone={traceStatuses[r.status].tone}>{traceStatuses[r.status].label}</StatusBadge>
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  )
}

export function TraceDetail({
  base,
  id,
  summary,
  onSelect,
  onClose,
}: {
  base: string
  id: number | null
  summary?: MessageTraceSummary
  onSelect: (id: number) => void
  onClose: () => void
}) {
  const detail = useQuery({
    queryKey: ["message-trace", base, "detail", id],
    queryFn: () => api<MessageTraceDetail>(`${base}/${id}`),
    enabled: id !== null,
  })
  const shown = detail.data ?? summary
  return (
    <Sheet open={id !== null} onOpenChange={(open) => !open && onClose()}>
      <SheetContent side="right" className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[640px]">
        <SheetHeader className="gap-2 border-b border-border p-6 pr-16">
          <SheetTitle className="break-words">{shown ? shown.subject || "No subject" : "Message"}</SheetTitle>
          <SheetDescription>{shown ? `Received ${formatDateTime(shown.receivedAt)}` : "Loading the delivery record"}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-1 flex-col gap-8 overflow-y-auto p-6">
          <QueryView query={detail} skeleton={<RowsSkeleton rows={6} />}>
            {(m) => (
              <div className="flex flex-col gap-8">
                <Trace m={m} onSelect={onSelect} />
              </div>
            )}
          </QueryView>
        </div>
      </SheetContent>
    </Sheet>
  )
}

"use client"

import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query"
import { ChevronRight, Download, Route, RotateCw, SearchX, TriangleAlert } from "lucide-react"
import { useState } from "react"

import { traceStatuses } from "@/components/admin/message-trace/labels"
import { TraceDetail } from "@/components/admin/message-trace/trace-detail"
import { Field } from "@/components/field"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { Spinner } from "@/components/spinner"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { useSession } from "@/hooks/use-session"
import { api, apiUrl } from "@/lib/api"
import { formatDateTime, formatShortDateTime } from "@/lib/format"
import type { MessageTracePage, MessageTraceStatus, MessageTraceSummary } from "@/lib/types"

const PAGE = 50

const periods = [
  { value: "1h", label: "Last hour", hours: 1 },
  { value: "24h", label: "Last 24 hours", hours: 24 },
  { value: "7d", label: "Last 7 days", hours: 168 },
  { value: "all", label: "Everything kept", hours: 0 },
  { value: "custom", label: "Custom range", hours: 0 },
]

const statusOptions: MessageTraceStatus[] = ["delivered", "junk", "queued", "deferred", "held", "bounced", "rejected", "discarded", "deleted"]

const textFields = [
  { key: "sender", label: "Sender", placeholder: "name@example.com or example.com" },
  { key: "recipient", label: "Recipient", placeholder: "Address, alias or domain" },
  { key: "subject", label: "Subject", placeholder: "Words in the subject" },
  { key: "messageId", label: "Message-ID", placeholder: "<id@mail.example.com>" },
] as const

type Text = Record<(typeof textFields)[number]["key"], string>

const emptyText: Text = { sender: "", recipient: "", subject: "", messageId: "" }

type Filters = Text & { status: string; period: string; from: string; to: string }

function traceQuery(f: Filters) {
  const hours = periods.find((p) => p.value === f.period)?.hours ?? 0
  const iso = (local: string) => (local ? new Date(local).toISOString() : undefined)
  return {
    sender: f.sender.trim(),
    recipient: f.recipient.trim(),
    subject: f.subject.trim(),
    messageId: f.messageId.trim(),
    status: f.status === "any" ? undefined : f.status,
    from: f.period === "custom" ? iso(f.from) : hours ? new Date(Date.now() - hours * 3_600_000).toISOString() : undefined,
    to: f.period === "custom" ? iso(f.to) : undefined,
  }
}

function Recipients({ list }: { list: string[] }) {
  if (list.length === 0) return <span className="text-fg-tertiary">Not recorded yet</span>
  return (
    <span className="flex min-w-0 items-baseline gap-2">
      <span className="truncate font-mono text-sm">{list[0]}</span>
      {list.length > 1 && <span className="shrink-0 text-sm text-fg-tertiary tabular-nums">+{list.length - 1}</span>}
    </span>
  )
}

function Sender({ address }: { address: string }) {
  return address ? <span className="truncate font-mono text-sm">{address}</span> : <span className="text-sm text-fg-tertiary">Delivery report</span>
}

export function MessageTraceTab({ orgId }: { orgId: string }) {
  const platformAdmin = !!useSession().data?.user.isPlatformAdmin
  const [scope, setScope] = useState<"org" | "server">("org")
  const [text, setText] = useState<Text>(emptyText)
  const [status, setStatus] = useState("any")
  const [period, setPeriod] = useState("7d")
  const [range, setRange] = useState({ from: "", to: "" })
  const [open, setOpen] = useState<{ id: number; summary?: MessageTraceSummary } | null>(null)
  const debounced = JSON.parse(useDebouncedValue(JSON.stringify(text), 300)) as Text
  const base = scope === "server" && platformAdmin ? "/admin/message-trace" : `/organizations/${orgId}/message-trace`
  const filters: Filters = { ...debounced, status, period, ...range }
  const filtered = Object.values(text).some((v) => v.trim()) || status !== "any" || period !== "7d"

  const trace = useInfiniteQuery({
    queryKey: ["message-trace", base, filters],
    queryFn: async ({ pageParam }) => {
      const page = await api<MessageTracePage>(base, { query: { ...traceQuery(filters), before: pageParam } })
      return { ...page, behind: !!page.readAt && Date.now() - Date.parse(page.readAt) > 10 * 60_000 }
    },
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => (last.messages.length === PAGE ? last.messages[last.messages.length - 1]?.id : undefined),
    placeholderData: keepPreviousData,
  })
  const first = trace.data?.pages[0]
  const messages = trace.data?.pages.flatMap((p) => p.messages) ?? []

  function clear() {
    setText(emptyText)
    setStatus("any")
    setPeriod("7d")
    setRange({ from: "", to: "" })
  }

  function exportCSV() {
    const link = document.createElement("a")
    link.href = apiUrl(`${base}/export`, traceQuery(filters))
    link.download = ""
    link.click()
  }

  const retention = first?.retentionDays
  const description =
    scope === "server"
      ? "Every message this server handled, including mail for domains no organization owns."
      : `Follow mail to and from this organization’s domains, from arrival to delivery.${retention ? ` Records are kept for ${retention} days.` : ""}`

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Message trace"
        description={description}
        actions={
          <>
            {platformAdmin && (
              <SegmentedControl
                label="Search scope"
                options={[
                  { value: "org", label: "This organization" },
                  { value: "server", label: "Whole server" },
                ]}
                value={scope}
                onChange={setScope}
              />
            )}
            <IconButton label="Refresh results" variant="secondary" onClick={() => trace.refetch()} disabled={trace.isFetching}>
              <RotateCw />
            </IconButton>
            <Button variant="secondary" onClick={exportCSV} disabled={messages.length === 0}>
              <Download aria-hidden />
              Export CSV
            </Button>
          </>
        }
      />

      {first?.behind && first.readAt && (
        <div role="status" className="flex gap-3 rounded-xl border border-warning/40 bg-warning/12 p-4 text-sm">
          <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-warning" />
          <p className="text-fg-secondary">
            <span className="font-semibold text-foreground">Recent mail may be missing.</span> The mail log was last read {formatDateTime(first.readAt)}.
            {platformAdmin ? " Check that the mail-trace service is running." : " Ask your server administrator to check message trace."}
          </p>
        </div>
      )}

      <form role="search" aria-label="Filter messages" onSubmit={(e) => e.preventDefault()} className="grid grid-cols-2 gap-4 rounded-xl border border-border bg-card p-4 lg:grid-cols-3">
        {textFields.map((f) => (
          <div key={f.key} className="max-sm:col-span-2">
            <Field id={`trace-${f.key}`} label={f.label}>
              <Input
                id={`trace-${f.key}`}
                type="search"
                autoComplete="off"
                spellCheck={false}
                placeholder={f.placeholder}
                className={f.key === "subject" ? undefined : "font-mono text-sm placeholder:font-sans placeholder:text-base"}
                value={text[f.key]}
                onChange={(e) => setText({ ...text, [f.key]: e.target.value })}
              />
            </Field>
          </div>
        ))}
        <Field id="trace-period" label="Received">
          <Select value={period} onValueChange={setPeriod}>
            <SelectTrigger id="trace-period" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {periods.map((p) => (
                <SelectItem key={p.value} value={p.value}>
                  {p.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field id="trace-status" label="Status">
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger id="trace-status" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="any">Any status</SelectItem>
              {statusOptions.map((s) => (
                <SelectItem key={s} value={s}>
                  {traceStatuses[s].label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {period === "custom" && (
          <>
            <Field id="trace-from" label="From">
              <Input id="trace-from" type="datetime-local" value={range.from} max={range.to || undefined} onChange={(e) => setRange({ ...range, from: e.target.value })} />
            </Field>
            <Field id="trace-to" label="Until">
              <Input id="trace-to" type="datetime-local" value={range.to} min={range.from || undefined} onChange={(e) => setRange({ ...range, to: e.target.value })} />
            </Field>
          </>
        )}
        <div className="col-span-2 flex min-h-8 items-center justify-between gap-3 lg:col-span-3">
          <p aria-live="polite" className="flex items-center gap-2 text-sm text-fg-tertiary">
            {trace.isFetching && !trace.isFetchingNextPage ? (
              <>
                <Spinner />
                Searching
              </>
            ) : trace.isSuccess ? (
              messages.length === 0 ? (
                "No matches"
              ) : (
                `${messages.length}${trace.hasNextPage ? "+" : ""} ${messages.length === 1 ? "message" : "messages"}, newest first`
              )
            ) : null}
          </p>
          {filtered && (
            <Button type="button" variant="quiet" size="sm" onClick={clear}>
              Clear filters
            </Button>
          )}
        </div>
      </form>

      {trace.isPending ? (
        <RowsSkeleton rows={6} />
      ) : trace.isError ? (
        <ErrorState error={trace.error} onRetry={() => trace.refetch()} />
      ) : messages.length === 0 ? (
        first?.readAt === null && !filtered ? (
          <EmptyState
            icon={Route}
            title="No mail recorded yet"
            description={
              platformAdmin
                ? "Records appear a few seconds after mail arrives once the mail-trace service reads the Postfix log. Start it with docker compose up -d mail-trace."
                : "Records appear a few seconds after mail is sent or received. If nothing shows up, ask your server administrator to turn on message trace."
            }
          />
        ) : (
          <EmptyState
            icon={SearchX}
            title="No messages match"
            description={filtered ? "Check the spelling of the address, choose a longer period or clear the filters." : "No mail was sent or received in the last 7 days."}
          />
        )
      ) : (
        <div className="animate-content flex flex-col gap-4">
          <div className="max-md:hidden">
            <Table className="table-fixed">
              <TableHeader>
                <TableRow>
                  <TableHead className="w-44">Received</TableHead>
                  <TableHead className="w-[22%]">Sender</TableHead>
                  <TableHead className="w-[22%]">Recipients</TableHead>
                  <TableHead>Subject</TableHead>
                  <TableHead className="w-44">Status</TableHead>
                  <TableHead className="w-14">
                    <span className="sr-only">Details</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {messages.map((m) => (
                  <TableRow key={m.id} className="cursor-pointer" onClick={() => setOpen({ id: m.id, summary: m })}>
                    <TableCell className="text-sm text-fg-secondary tabular-nums">
                      <time dateTime={m.receivedAt}>{formatShortDateTime(new Date(m.receivedAt))}</time>
                    </TableCell>
                    <TableCell>
                      <span className="flex min-w-0">
                        <Sender address={m.sender} />
                      </span>
                    </TableCell>
                    <TableCell>
                      <Recipients list={m.recipients} />
                    </TableCell>
                    <TableCell className="truncate">{m.subject || <span className="text-fg-tertiary">No subject</span>}</TableCell>
                    <TableCell>
                      <StatusBadge tone={traceStatuses[m.status].tone}>{traceStatuses[m.status].label}</StatusBadge>
                    </TableCell>
                    <TableCell className="px-2 text-right">
                      <IconButton
                        label={`Trace ${m.subject ? `“${m.subject}”` : "this message"}`}
                        size="icon-sm"
                        onClick={(e) => {
                          e.stopPropagation()
                          setOpen({ id: m.id, summary: m })
                        }}
                      >
                        <ChevronRight />
                      </IconButton>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-xl border border-border bg-card md:hidden">
            {messages.map((m) => (
              <li key={m.id}>
                <button type="button" onClick={() => setOpen({ id: m.id, summary: m })} className="flex w-full flex-col gap-2 px-4 py-3 text-left transition-colors duration-160 active:bg-muted/50">
                  <span className="flex items-start justify-between gap-3">
                    <span className="min-w-0 flex-1 truncate font-semibold">{m.subject || <span className="font-normal text-fg-tertiary">No subject</span>}</span>
                    <StatusBadge tone={traceStatuses[m.status].tone}>{traceStatuses[m.status].label}</StatusBadge>
                  </span>
                  <span className="flex min-w-0 flex-col gap-1 text-fg-secondary">
                    <span className="flex min-w-0 gap-2">
                      <span className="w-8 shrink-0 text-label text-fg-tertiary">From</span>
                      <Sender address={m.sender} />
                    </span>
                    <span className="flex min-w-0 gap-2">
                      <span className="w-8 shrink-0 text-label text-fg-tertiary">To</span>
                      <Recipients list={m.recipients} />
                    </span>
                  </span>
                  <time dateTime={m.receivedAt} className="text-xs text-fg-tertiary tabular-nums">
                    {formatShortDateTime(new Date(m.receivedAt))}
                  </time>
                </button>
              </li>
            ))}
          </ul>
          {trace.hasNextPage && (
            <Button variant="secondary" className="self-center" loading={trace.isFetchingNextPage} onClick={() => trace.fetchNextPage()}>
              Load older messages
            </Button>
          )}
        </div>
      )}

      <TraceDetail base={base} id={open?.id ?? null} summary={open?.summary} onSelect={(id) => setOpen({ id })} onClose={() => setOpen(null)} />
    </section>
  )
}

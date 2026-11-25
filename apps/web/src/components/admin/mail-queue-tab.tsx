"use client"

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Ellipsis, FunnelX, MailCheck, PanelRightOpen, Pause, Play, RefreshCw, RotateCw, Trash2, Unplug } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { api, ApiError, errorMessage } from "@/lib/api"
import { formatBytes, formatDateTime, formatRelative, plural } from "@/lib/format"
import type { MailQueue, QueueAction, QueuedMessage, QueueFilter, QueueName } from "@/lib/types"

const LIMIT = 200

const statuses: Record<QueueName, { label: string; tone: "warning" | "neutral" | "info" }> = {
  deferred: { label: "Deferred", tone: "warning" },
  hold: { label: "On hold", tone: "neutral" },
  active: { label: "Delivering", tone: "info" },
  incoming: { label: "Incoming", tone: "neutral" },
  maildrop: { label: "Incoming", tone: "neutral" },
}

type Segment = "all" | "deferred" | "hold"

const segments: { value: Segment; label: string; empty: string }[] = [
  { value: "all", label: "All", empty: "No queued messages match" },
  { value: "deferred", label: "Deferred", empty: "No deferred messages match" },
  { value: "hold", label: "On hold", empty: "No held messages match" },
]

const noFilters = { sender: "", recipientDomain: "", error: "" }

type Target = { ids: string[] } | { filter: QueueFilter }

type Run = { action: QueueAction; target: Target; from: "bulk" | "single" }

const outcomes: Record<QueueAction, { done: (n: number) => string; none: string }> = {
  retry: { done: (n) => `Retrying ${plural(n, "message")} now`, none: "Nothing to retry. Only deferred messages can be retried." },
  hold: { done: (n) => `Put ${plural(n, "message")} on hold`, none: "Nothing to hold. The messages are already on hold or have left the queue." },
  release: { done: (n) => `Released ${plural(n, "message")} from hold`, none: "Nothing to release. None of the messages are on hold." },
  delete: { done: (n) => `Deleted ${plural(n, "message")}`, none: "Nothing to delete. The messages have already left the queue." },
}

const senderLabel = (m: QueuedMessage) => m.sender || "MAILER-DAEMON"

const firstError = (m: QueuedMessage) => m.recipients.find((r) => r.delayReason)?.delayReason

const nextAttemptLabel = (m: QueuedMessage) => {
  if (!m.nextAttempt) return "—"
  return new Date(m.nextAttempt).getTime() <= Date.now() ? "Due now" : formatRelative(m.nextAttempt)
}

function SelectBox({ label, checked, indeterminate, onChange }: { label: string; checked: boolean; indeterminate?: boolean; onChange: () => void }) {
  return (
    <label className="-my-2 flex size-8 cursor-pointer items-center justify-center max-md:size-11">
      <input
        type="checkbox"
        aria-label={label}
        checked={checked}
        ref={(el) => {
          if (el) el.indeterminate = !!indeterminate
        }}
        onChange={onChange}
        className="size-4 cursor-pointer accent-ember"
      />
    </label>
  )
}

function Fact({ label, wide, children }: { label: string; wide?: boolean; children: React.ReactNode }) {
  return (
    <div className={wide ? "flex flex-col gap-1 sm:col-span-2" : "flex flex-col gap-1"}>
      <dt className="text-label text-fg-tertiary">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

export function MailQueueTab() {
  const qc = useQueryClient()
  const [segment, setSegment] = useState<Segment>("all")
  const [inputs, setInputs] = useState(noFilters)
  const typed = useDebouncedValue(inputs, 300)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [allMatching, setAllMatching] = useState(false)
  const [openId, setOpenId] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<{ target: Target; count: number; from: Run["from"] } | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const filter: QueueFilter = {
    sender: typed.sender.trim() || undefined,
    recipientDomain: typed.recipientDomain.trim() || undefined,
    error: typed.error.trim() || undefined,
    queue: segment === "all" ? undefined : segment,
  }
  const listing = useQuery({
    queryKey: ["mail-queue", filter],
    queryFn: () => api<MailQueue>("/admin/mail-queue", { query: { ...filter, limit: LIMIT } }),
    placeholderData: keepPreviousData,
    refetchInterval: selected.size > 0 || allMatching ? false : 15_000,
  })

  const act = useMutation({
    mutationFn: ({ action, target }: Run) => api<{ affected: number }>(`/admin/mail-queue/${action}`, { method: "POST", body: target }),
    onSuccess: ({ affected }, { action, from }) => {
      if (affected) toast.success(outcomes[action].done(affected))
      else toast(outcomes[action].none)
      setDeleting(null)
      if (from === "bulk") {
        setSelected(new Set())
        setAllMatching(false)
      } else {
        setOpenId(null)
      }
      return qc.invalidateQueries({ queryKey: ["mail-queue"] })
    },
  })

  const data = listing.data
  const messages = data?.messages ?? []
  const picked = messages.filter((m) => allMatching || selected.has(m.id))
  const allShown = messages.length > 0 && picked.length === messages.length
  const typing = Object.values(inputs).some((v) => v.trim())
  const narrowed = typing || segment !== "all"
  const hasFilter = !!(filter.sender || filter.recipientDomain || filter.error || filter.queue)
  const count = allMatching ? (data?.matched ?? 0) : picked.length
  const bulkTarget: Target = allMatching ? { filter } : { ids: picked.map((m) => m.id) }
  const open = messages.find((m) => m.id === openId)
  const pending = (action: QueueAction, from: Run["from"]) => act.isPending && act.variables.action === action && act.variables.from === from

  function clearSelection() {
    setSelected(new Set())
    setAllMatching(false)
  }

  function changeSegment(next: Segment) {
    setSegment(next)
    clearSelection()
  }

  function changeInput(key: keyof typeof noFilters, value: string) {
    setInputs((current) => ({ ...current, [key]: value }))
    clearSelection()
  }

  function clearFilters() {
    setInputs(noFilters)
    setSegment("all")
    clearSelection()
  }

  function toggle(id: string) {
    const next = new Set(allMatching ? messages.map((m) => m.id) : selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setAllMatching(false)
    setSelected(next)
  }

  function run(action: QueueAction, target: Target, n: number, from: Run["from"]) {
    if (action === "delete") setDeleting({ target, count: n, from })
    else act.mutate({ action, target, from })
  }

  async function refresh() {
    setRefreshing(true)
    await listing.refetch()
    setRefreshing(false)
  }

  const notConfigured = listing.error instanceof ApiError && listing.error.code === "queue_not_configured"
  const summary = data?.summary
  const segmentOptions = segments.map((s) => {
    const n = !summary ? undefined : s.value === "all" ? summary.total : summary.queues[s.value]
    return {
      value: s.value,
      label: (
        <>
          {s.label}
          {n !== undefined && <span className="font-normal text-fg-tertiary tabular-nums">{n.toLocaleString()}</span>}
        </>
      ),
    }
  })
  const bulk = [
    { action: "retry" as const, label: "Retry now", icon: RotateCw, enabled: allMatching || picked.some((m) => m.queue === "deferred") },
    { action: "hold" as const, label: "Hold", icon: Pause, enabled: allMatching || picked.some((m) => m.queue !== "hold") },
    { action: "release" as const, label: "Release", icon: Play, enabled: allMatching || picked.some((m) => m.queue === "hold") },
    { action: "delete" as const, label: "Delete", icon: Trash2, enabled: count > 0 },
  ]

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Mail queue"
        description="Mail that Postfix accepted but has not delivered yet, oldest first. Only the envelope is shown; message content stays private."
        actions={
          !notConfigured && (
            <Button variant="secondary" loading={refreshing} disabled={listing.isPending} onClick={refresh}>
              <RefreshCw aria-hidden />
              Refresh
            </Button>
          )
        }
      />

      {notConfigured ? (
        <EmptyState
          icon={Unplug}
          title="The mail queue is not connected"
          description={
            <>
              Add <code className="font-mono text-sm whitespace-nowrap">AETHER_POSTFIX_API_KEY</code> to <code className="font-mono text-sm whitespace-nowrap">.env</code>, then run{" "}
              <code className="font-mono text-sm whitespace-nowrap">docker compose up -d</code> to recreate the api and postfix containers. New installations get the key from{" "}
              <code className="font-mono text-sm whitespace-nowrap">scripts/setup.sh</code>.
            </>
          }
        />
      ) : listing.isError && !data ? (
        <ErrorState error={listing.error} onRetry={() => listing.refetch()} />
      ) : (
        <>
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <div className="-mx-1 overflow-x-auto px-1 py-1">
                <div className="w-max">
                  <SegmentedControl label="Queue" options={segmentOptions} value={segment} onChange={changeSegment} />
                </div>
              </div>
              {summary?.oldestArrival && (
                <p className="text-sm text-fg-tertiary">
                  Oldest message not on hold arrived{" "}
                  <time dateTime={summary.oldestArrival} title={formatDateTime(summary.oldestArrival)} className="font-semibold text-fg-secondary">
                    {formatRelative(summary.oldestArrival)}
                  </time>
                </p>
              )}
            </div>
            <div className="grid gap-4 sm:grid-cols-3">
              <Field id="queue-sender" label="Sender">
                <Input id="queue-sender" type="search" placeholder="alice@example.com" autoComplete="off" spellCheck={false} value={inputs.sender} onChange={(e) => changeInput("sender", e.target.value)} />
              </Field>
              <Field id="queue-domain" label="Recipient domain">
                <Input id="queue-domain" type="search" placeholder="gmail.com" autoComplete="off" spellCheck={false} value={inputs.recipientDomain} onChange={(e) => changeInput("recipientDomain", e.target.value)} />
              </Field>
              <Field id="queue-error" label="Delivery error">
                <Input id="queue-error" type="search" placeholder="Connection refused" autoComplete="off" spellCheck={false} value={inputs.error} onChange={(e) => changeInput("error", e.target.value)} />
              </Field>
            </div>
          </div>

          <div role="group" aria-label="Selected messages" className="flex flex-wrap items-center gap-3 rounded-xl border border-border bg-card px-4 py-3">
            <div aria-live="polite" className="flex min-h-8 min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1 text-sm">
              {count > 0 ? (
                <>
                  <span className="font-semibold">{allMatching ? `All ${plural(count, "matching message")} selected` : `${count.toLocaleString()} selected`}</span>
                  {allShown && !allMatching && data && data.matched > messages.length && (
                    hasFilter ? (
                      <Button variant="link" size="sm" disabled={listing.isPlaceholderData} onClick={() => setAllMatching(true)}>
                        Select all {plural(data.matched, "matching message")}
                      </Button>
                    ) : (
                      <span className="text-fg-tertiary">Filter the list to act on more than the {messages.length} shown.</span>
                    )
                  )}
                  <Button variant="link" size="sm" onClick={clearSelection}>
                    Clear selection
                  </Button>
                </>
              ) : listing.isError ? (
                <span className="text-danger">The queue could not be refreshed, so this list may be out of date. {errorMessage(listing.error)}</span>
              ) : (
                <span className="text-fg-secondary">
                  {data ? (data.matched > messages.length ? `Showing the oldest ${messages.length} of ${plural(data.matched, "message")}` : plural(data.matched, "message")) : "Loading the queue"}
                  <span className="text-fg-tertiary max-sm:hidden"> · Select messages to retry, hold, release or delete them.</span>
                </span>
              )}
            </div>
            <div className="flex flex-wrap gap-2">
              {bulk.map(({ action, label, icon: Icon, enabled }) => (
                <Button
                  key={action}
                  variant="secondary"
                  size="sm"
                  className={action === "delete" ? "text-danger" : undefined}
                  disabled={!enabled || act.isPending}
                  loading={pending(action, "bulk")}
                  onClick={() => run(action, bulkTarget, count, "bulk")}
                >
                  <Icon aria-hidden />
                  <span className="max-sm:sr-only">{label}</span>
                </Button>
              ))}
            </div>
          </div>

          {listing.isPending ? (
            <RowsSkeleton rows={6} />
          ) : messages.length === 0 ? (
            narrowed ? (
              <EmptyState
                icon={FunnelX}
                title={segments.find((s) => s.value === segment)?.empty ?? "No queued messages match"}
                description="Nothing in the queue matches right now. The list refreshes every 15 seconds."
                action={
                  <Button variant="secondary" onClick={clearFilters}>
                    Clear filters
                  </Button>
                }
              />
            ) : (
              <EmptyState
                icon={MailCheck}
                title="The mail queue is empty"
                description="Postfix has delivered or bounced everything it accepted. Messages wait here while a receiving server is down, slow or refusing them."
              />
            )
          ) : (
            <div className={listing.isPlaceholderData ? "opacity-60 transition-opacity duration-160" : "animate-content"}>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-12 pr-0">
                      <SelectBox label="Select all shown messages" checked={allShown} indeterminate={picked.length > 0 && !allShown} onChange={() => (allShown ? clearSelection() : setSelected(new Set(messages.map((m) => m.id))))} />
                    </TableHead>
                    <TableHead>Message</TableHead>
                    <TableHead className="max-sm:hidden">Status</TableHead>
                    <TableHead className="max-lg:hidden">Delivery error</TableHead>
                    <TableHead className="max-sm:hidden">Arrived</TableHead>
                    <TableHead className="max-md:hidden">Next attempt</TableHead>
                    <TableHead className="text-right max-md:hidden">Size</TableHead>
                    <TableHead className="w-14">
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {messages.map((m) => {
                    const error = firstError(m)
                    const isPicked = allMatching || selected.has(m.id)
                    return (
                      <TableRow key={m.id} data-state={isPicked ? "selected" : undefined}>
                        <TableCell className="pr-0">
                          <SelectBox label={`Select the message from ${senderLabel(m)}`} checked={isPicked} onChange={() => toggle(m.id)} />
                        </TableCell>
                        <TableCell>
                          <button
                            type="button"
                            onClick={() => setOpenId(m.id)}
                            className="block max-w-52 truncate rounded-sm text-left font-mono text-sm font-medium underline-offset-3 hover:underline sm:max-w-72"
                          >
                            {senderLabel(m)}
                          </button>
                          <p className="max-w-52 truncate text-sm text-fg-tertiary sm:max-w-72">
                            to <span className="font-mono">{m.recipients[0]?.address}</span>
                            {m.recipients.length > 1 && ` and ${m.recipients.length - 1} more`}
                          </p>
                          <div className="mt-2 flex items-center gap-2 text-sm text-fg-tertiary sm:hidden">
                            <StatusBadge tone={statuses[m.queue].tone}>{statuses[m.queue].label}</StatusBadge>
                            <time dateTime={m.arrivalTime}>{formatRelative(m.arrivalTime)}</time>
                          </div>
                        </TableCell>
                        <TableCell className="max-sm:hidden">
                          <StatusBadge tone={statuses[m.queue].tone}>{statuses[m.queue].label}</StatusBadge>
                        </TableCell>
                        <TableCell className="max-lg:hidden">
                          <p title={error} className="max-w-96 truncate text-sm text-fg-secondary">
                            {error ?? "—"}
                          </p>
                        </TableCell>
                        <TableCell className="text-sm max-sm:hidden">
                          <time dateTime={m.arrivalTime} title={formatDateTime(m.arrivalTime)}>
                            {formatRelative(m.arrivalTime)}
                          </time>
                        </TableCell>
                        <TableCell className="text-sm max-md:hidden">{nextAttemptLabel(m)}</TableCell>
                        <TableCell className="text-right text-sm tabular-nums max-md:hidden">{formatBytes(m.size)}</TableCell>
                        <TableCell>
                          <DropdownMenu>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <DropdownMenuTrigger asChild>
                                  <Button variant="quiet" size="icon-sm" aria-label={`Actions for the message from ${senderLabel(m)}`}>
                                    <Ellipsis />
                                  </Button>
                                </DropdownMenuTrigger>
                              </TooltipTrigger>
                              <TooltipContent>Actions</TooltipContent>
                            </Tooltip>
                            <DropdownMenuContent align="end" className="w-auto min-w-48">
                              <DropdownMenuItem onSelect={() => setOpenId(m.id)}>
                                <PanelRightOpen aria-hidden />
                                View details
                              </DropdownMenuItem>
                              <DropdownMenuSeparator />
                              {m.queue === "deferred" && (
                                <DropdownMenuItem onSelect={() => run("retry", { ids: [m.id] }, 1, "single")}>
                                  <RotateCw aria-hidden />
                                  Retry now
                                </DropdownMenuItem>
                              )}
                              {m.queue === "hold" ? (
                                <DropdownMenuItem onSelect={() => run("release", { ids: [m.id] }, 1, "single")}>
                                  <Play aria-hidden />
                                  Release
                                </DropdownMenuItem>
                              ) : (
                                <DropdownMenuItem onSelect={() => run("hold", { ids: [m.id] }, 1, "single")}>
                                  <Pause aria-hidden />
                                  Hold
                                </DropdownMenuItem>
                              )}
                              <DropdownMenuItem variant="destructive" onSelect={() => run("delete", { ids: [m.id] }, 1, "single")}>
                                <Trash2 aria-hidden />
                                Delete…
                              </DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>
          )}
        </>
      )}

      <Sheet open={!!openId} onOpenChange={(next) => !next && setOpenId(null)}>
        <SheetContent side="right" className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[560px]">
          <SheetHeader className="border-b border-border p-6">
            <SheetTitle>Queued message</SheetTitle>
            <SheetDescription className="font-mono text-sm">{openId}</SheetDescription>
          </SheetHeader>
          {open ? (
            <>
              <div className="flex flex-1 flex-col gap-6 overflow-y-auto p-6 text-base">
                <dl className="grid gap-4 sm:grid-cols-2">
                  <Fact label="Status">
                    <StatusBadge tone={statuses[open.queue].tone}>{statuses[open.queue].label}</StatusBadge>
                  </Fact>
                  <Fact label="Size">{formatBytes(open.size)}</Fact>
                  <Fact label="Arrived">
                    <time dateTime={open.arrivalTime}>{formatDateTime(open.arrivalTime)}</time>
                    <span className="block text-sm text-fg-tertiary">{formatRelative(open.arrivalTime)}</span>
                  </Fact>
                  <Fact label="Next attempt">
                    {open.nextAttempt ? (
                      <>
                        <time dateTime={open.nextAttempt}>{formatDateTime(open.nextAttempt)}</time>
                        <span className="block text-sm text-fg-tertiary">{nextAttemptLabel(open)}</span>
                      </>
                    ) : open.queue === "hold" ? (
                      "None while on hold"
                    ) : (
                      "—"
                    )}
                  </Fact>
                  <Fact label="Sender" wide>
                    <span className="font-mono text-sm break-all">{senderLabel(open)}</span>
                  </Fact>
                </dl>
                <div className="flex flex-col gap-3">
                  <h3 className="text-label text-fg-tertiary">Recipients ({open.recipients.length})</h3>
                  <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
                    {open.recipients.map((r) => (
                      <li key={r.address} className="flex flex-col gap-2 px-4 py-3">
                        <span className="font-mono text-sm break-all">{r.address}</span>
                        {r.delayReason ? (
                          <p className="rounded-md bg-sunken px-3 py-2 font-mono text-xs break-words whitespace-pre-wrap text-fg-secondary">{r.delayReason}</p>
                        ) : (
                          <p className="text-sm text-fg-tertiary">No delivery attempt has failed for this recipient yet.</p>
                        )}
                      </li>
                    ))}
                  </ul>
                </div>
              </div>
              <SheetFooter className="flex-row flex-wrap gap-3 border-t border-border p-6">
                {open.queue === "deferred" && (
                  <Button variant="secondary" disabled={act.isPending} loading={pending("retry", "single")} onClick={() => run("retry", { ids: [open.id] }, 1, "single")}>
                    <RotateCw aria-hidden />
                    Retry now
                  </Button>
                )}
                {open.queue === "hold" ? (
                  <Button variant="secondary" disabled={act.isPending} loading={pending("release", "single")} onClick={() => run("release", { ids: [open.id] }, 1, "single")}>
                    <Play aria-hidden />
                    Release
                  </Button>
                ) : (
                  <Button variant="secondary" disabled={act.isPending} loading={pending("hold", "single")} onClick={() => run("hold", { ids: [open.id] }, 1, "single")}>
                    <Pause aria-hidden />
                    Hold
                  </Button>
                )}
                <Button variant="secondary" className="text-danger sm:ml-auto" disabled={act.isPending} onClick={() => run("delete", { ids: [open.id] }, 1, "single")}>
                  <Trash2 aria-hidden />
                  Delete
                </Button>
              </SheetFooter>
            </>
          ) : (
            <EmptyState
              icon={MailCheck}
              title="No longer in this view"
              description="Postfix delivered or bounced this message, or moved it to another queue, since the list was loaded."
              action={
                <Button variant="secondary" onClick={() => setOpenId(null)}>
                  Close
                </Button>
              }
            />
          )}
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(next) => !next && setDeleting(null)}
        title={deleting && "filter" in deleting.target ? `Delete all ${plural(deleting.count, "matching message")}?` : deleting?.count === 1 ? "Delete this message?" : `Delete ${plural(deleting?.count ?? 0, "message")}?`}
        description={
          deleting && "filter" in deleting.target
            ? "Every message that matches the current filters when you confirm is deleted, including messages that arrive in the meantime. Postfix does not tell the senders or the recipients. This cannot be undone."
            : deleting?.count === 1
              ? "Postfix removes it from the queue without telling the sender or the recipients. This cannot be undone."
              : "Postfix removes them from the queue without telling the senders or the recipients. This cannot be undone."
        }
        typedConfirmation={deleting && "filter" in deleting.target ? "delete" : undefined}
        confirmLabel={deleting?.count === 1 ? "Delete message" : `Delete ${plural(deleting?.count ?? 0, "message")}`}
        pending={act.isPending}
        onConfirm={() => deleting && act.mutate({ action: "delete", target: deleting.target, from: deleting.from })}
      />
    </section>
  )
}

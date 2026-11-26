"use client"

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowDownLeft, ArrowUpRight, ChartColumn, ChevronDown, Clock, Download, HardDrive, Inbox, RefreshCw } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { LastSignIn, QuotaUsage } from "@/components/admin/usage-cells"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { api, apiUrl } from "@/lib/api"
import { formatBytes, formatDate, formatDateTime, plural } from "@/lib/format"
import type { OrgReport, ReportActivity, VolumePoint } from "@/lib/types"

const periodOptions = [7, 30, 90].map((days) => ({ value: days, label: `${days} days` }))
const inactivePresets = [30, 60, 90].map((days) => ({ value: days, label: `${days}d` }))

function formatDayShort(day: string) {
  const [y, m, d] = day.split("-").map(Number)
  return new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", timeZone: "UTC" }).format(new Date(Date.UTC(y, m - 1, d)))
}

function formatDayFull(day: string) {
  const [y, m, d] = day.split("-").map(Number)
  return new Intl.DateTimeFormat(undefined, { day: "numeric", month: "long", year: "numeric", timeZone: "UTC" }).format(new Date(Date.UTC(y, m - 1, d)))
}

function SummaryCard({
  label,
  value,
  detail,
  icon: Icon,
  tone,
}: {
  label: string
  value: string
  detail: string
  icon: React.ComponentType<{ className?: string }>
  tone?: "warning"
}) {
  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-5">
      <div className="flex items-center justify-between gap-2">
        <span className="text-label text-fg-tertiary">{label}</span>
        <Icon aria-hidden className={cn("size-4 text-fg-tertiary", tone === "warning" && "text-warning")} />
      </div>
      <div className="flex flex-col gap-1">
        <span className="font-display text-2xl font-bold tracking-tight tabular-nums">{value}</span>
        <span className="text-sm text-fg-secondary tabular-nums">{detail}</span>
      </div>
    </div>
  )
}

function VolumeChart({ series, days, orgId }: { series: VolumePoint[]; days: number; orgId: string }) {
  const [activeIndex, setActiveIndex] = useState<number | null>(null)
  const maxVal = series.reduce((acc, p) => Math.max(acc, p.sentCount, p.receivedCount), 0)
  const active = activeIndex !== null ? series[activeIndex] ?? null : null
  const width = 760
  const height = 220
  const padLeft = 44
  const padRight = 12
  const padTop = 16
  const padBottom = 28
  const plotW = width - padLeft - padRight
  const plotH = height - padTop - padBottom
  const yMax = maxVal <= 0 ? 4 : maxVal <= 4 ? 4 : Math.ceil(maxVal / 4) * 4
  const yTicks = [0, 0.25, 0.5, 0.75, 1].map((t) => Math.round(yMax * t))
  const groupW = series.length > 0 ? plotW / series.length : plotW
  const barGap = groupW > 14 ? 2 : 1
  const barW = Math.max(2, Math.min(14, (groupW - barGap * 3) / 2))
  const labelEvery = series.length <= 10 ? 1 : series.length <= 31 ? 5 : 15

  return (
    <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5 sm:p-6">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex flex-col gap-1">
          <h3 className="text-h3">Mail volume over time</h3>
          <p className="text-sm text-fg-tertiary tabular-nums">
            {active
              ? `${formatDayFull(active.day)} · ${plural(active.sentCount, "sent message")} (${formatBytes(active.sentBytes)}) · ${plural(active.receivedCount, "received message")} (${formatBytes(active.receivedBytes)})`
              : `Daily messages sent and received across this organization over the last ${days} days.`}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-4">
          <div className="flex items-center gap-4 text-xs text-fg-secondary">
            <span className="inline-flex items-center gap-1.5">
              <span aria-hidden className="size-2.5 rounded-xs bg-primary" />
              Sent
            </span>
            <span className="inline-flex items-center gap-1.5">
              <span aria-hidden className="size-2.5 rounded-xs bg-info" />
              Received
            </span>
          </div>
          <Button variant="quiet" size="sm" asChild>
            <a href={apiUrl(`/organizations/${orgId}/reports/volume`, { format: "csv", days })} download>
              <Download aria-hidden />
              CSV
            </a>
          </Button>
        </div>
      </div>

      {maxVal === 0 ? (
        <div className="flex flex-col items-center justify-center gap-2 rounded-lg bg-sunken px-6 py-10 text-center">
          <ChartColumn aria-hidden className="size-6 text-fg-tertiary" />
          <p className="font-medium">No mail recorded in the last {days} days</p>
          <p className="max-w-[46ch] text-sm text-fg-secondary">
            Sent and received counts appear here as messages pass through the mail server.
          </p>
        </div>
      ) : (
        <div className="w-full overflow-x-auto">
          <svg
            role="img"
            aria-label={`Mail volume over the last ${days} days`}
            viewBox={`0 0 ${width} ${height}`}
            className="h-56 w-full min-w-[520px] select-none"
            onMouseLeave={() => setActiveIndex(null)}
          >
            {yTicks.map((tick) => {
              const y = padTop + plotH - (tick / yMax) * plotH
              return (
                <g key={tick}>
                  <line x1={padLeft} y1={y} x2={width - padRight} y2={y} stroke="var(--border)" strokeDasharray={tick === 0 ? undefined : "3 3"} />
                  <text x={padLeft - 8} y={y + 4} textAnchor="end" fontSize="11" fill="var(--fg-tertiary)" className="tabular-nums">
                    {tick}
                  </text>
                </g>
              )
            })}
            {series.map((p, i) => {
              const cx = padLeft + i * groupW + groupW / 2
              const sentH = p.sentCount > 0 ? Math.max(2, (p.sentCount / yMax) * plotH) : 0
              const recvH = p.receivedCount > 0 ? Math.max(2, (p.receivedCount / yMax) * plotH) : 0
              const isHighlighted = activeIndex === i
              const showLabel = i % labelEvery === 0 || i === series.length - 1
              return (
                <g
                  key={p.day}
                  tabIndex={0}
                  role="button"
                  aria-label={`${formatDayFull(p.day)}: ${p.sentCount} sent, ${p.receivedCount} received`}
                  className="cursor-pointer outline-none"
                  onMouseEnter={() => setActiveIndex(i)}
                  onFocus={() => setActiveIndex(i)}
                  onBlur={() => setActiveIndex((prev) => (prev === i ? null : prev))}
                >
                  <rect
                    x={padLeft + i * groupW}
                    y={padTop}
                    width={groupW}
                    height={plotH}
                    fill={isHighlighted ? "var(--muted)" : "transparent"}
                    opacity={isHighlighted ? 0.55 : 0}
                    rx={4}
                  />
                  {sentH > 0 && (
                    <rect
                      x={cx - barW - barGap / 2}
                      y={padTop + plotH - sentH}
                      width={barW}
                      height={sentH}
                      rx={2}
                      fill="var(--primary)"
                    />
                  )}
                  {recvH > 0 && (
                    <rect
                      x={cx + barGap / 2}
                      y={padTop + plotH - recvH}
                      width={barW}
                      height={recvH}
                      rx={2}
                      fill="var(--info)"
                    />
                  )}
                  {showLabel && (
                    <text x={cx} y={height - 6} textAnchor="middle" fontSize="11" fill="var(--fg-tertiary)">
                      {formatDayShort(p.day)}
                    </text>
                  )}
                </g>
              )
            })}
          </svg>
        </div>
      )}
    </section>
  )
}

function ActivityTable({
  title,
  description,
  items,
  direction,
  orgId,
  days,
}: {
  title: string
  description: string
  items: ReportActivity[]
  direction: "sent" | "received"
  orgId: string
  days: number
}) {
  const maxCount = items.reduce((acc, item) => Math.max(acc, direction === "sent" ? item.sentCount : item.receivedCount), 0)

  return (
    <section className="flex flex-col gap-4">
      <div className="flex items-end justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h3 className="text-h3">{title}</h3>
          <p className="text-sm text-fg-tertiary">{description}</p>
        </div>
        <Button variant="quiet" size="sm" asChild>
          <a href={apiUrl(`/organizations/${orgId}/reports/activity`, { format: "csv", days, sort: direction })} download>
            <Download aria-hidden />
            CSV
          </a>
        </Button>
      </div>
      {items.length === 0 ? (
        <div className="rounded-xl border border-border bg-card">
          <EmptyState
            icon={direction === "sent" ? ArrowUpRight : ArrowDownLeft}
            title={direction === "sent" ? "No messages sent" : "No messages received"}
            description={`No mailboxes ${direction === "sent" ? "sent" : "received"} mail in the last ${days} days.`}
          />
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Mailbox</TableHead>
              <TableHead className="text-right">{direction === "sent" ? "Sent" : "Received"}</TableHead>
              <TableHead className="text-right">Volume</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((item) => {
              const count = direction === "sent" ? item.sentCount : item.receivedCount
              const bytes = direction === "sent" ? item.sentBytes : item.receivedBytes
              const pct = maxCount > 0 ? Math.max(4, Math.round((count / maxCount) * 100)) : 0
              return (
                <TableRow key={item.id}>
                  <TableCell>
                    <div className="flex flex-col gap-0.5">
                      <div className="flex items-center gap-2">
                        <span className="font-mono text-sm">{item.address}</span>
                        {item.kind === "shared" && <StatusBadge tone="neutral">Shared</StatusBadge>}
                      </div>
                      {item.displayName && <span className="text-xs text-fg-tertiary">{item.displayName}</span>}
                    </div>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    <div className="flex flex-col items-end gap-1">
                      <span className="font-medium">{count.toLocaleString()}</span>
                      <div className="h-1.5 w-20 overflow-hidden rounded-full bg-muted">
                        <div className={cn("h-full rounded-full", direction === "sent" ? "bg-primary" : "bg-info")} style={{ width: `${pct}%` }} />
                      </div>
                    </div>
                  </TableCell>
                  <TableCell className="text-right text-sm text-fg-secondary tabular-nums">{formatBytes(bytes)}</TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}
    </section>
  )
}

export function ReportsTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const [days, setDays] = useState(30)
  const [inactiveInput, setInactiveInput] = useState("30")
  const debouncedInactive = useDebouncedValue(inactiveInput, 300)
  const parsedInactive = Number.parseInt(debouncedInactive, 10)
  const inactiveDays = Number.isFinite(parsedInactive) && parsedInactive >= 1 && parsedInactive <= 3650 ? parsedInactive : 30

  const report = useQuery({
    queryKey: ["reports", orgId, days, inactiveDays],
    queryFn: () => api<OrgReport>(`/organizations/${orgId}/reports`, { query: { days, inactiveDays, limit: 25 } }),
    placeholderData: keepPreviousData,
  })

  const refreshSizes = useMutation({
    mutationFn: () => api<{ updated: number }>(`/organizations/${orgId}/reports/refresh`, { method: "POST" }),
    onSuccess: (r) => {
      toast.success(`Updated sizes for ${plural(r.updated, "mailbox")}`)
      qc.invalidateQueries({ queryKey: ["reports", orgId] })
      qc.invalidateQueries({ queryKey: ["mailboxes", orgId] })
    },
  })

  const updatedNote = report.data?.storageUpdatedAt
    ? `Sizes updated ${formatDateTime(report.data.storageUpdatedAt)}.`
    : "Mailbox sizes are refreshed periodically from the mail server."

  return (
    <section className="flex flex-col gap-8">
      <SectionHeader
        title="Reports"
        description={`Storage use, sign-in activity and mail volume for this organization. ${updatedNote}`}
        actions={
          <>
            <SegmentedControl label="Report period" options={periodOptions} value={days} onChange={setDays} />
            <Button variant="secondary" onClick={() => refreshSizes.mutate()} loading={refreshSizes.isPending}>
              <RefreshCw aria-hidden />
              Refresh sizes
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="secondary">
                  <Download aria-hidden />
                  Export CSV
                  <ChevronDown aria-hidden />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-60">
                <DropdownMenuItem asChild>
                  <a href={apiUrl(`/organizations/${orgId}/reports/mailboxes`, { format: "csv", days })} download>
                    All mailboxes
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <a href={apiUrl(`/organizations/${orgId}/reports/inactive`, { format: "csv", inactiveDays })} download>
                    Inactive mailboxes ({inactiveDays}d)
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <a href={apiUrl(`/organizations/${orgId}/reports/largest`, { format: "csv" })} download>
                    Largest mailboxes
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <a href={apiUrl(`/organizations/${orgId}/reports/activity`, { format: "csv", days })} download>
                    Top senders and receivers ({days}d)
                  </a>
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <a href={apiUrl(`/organizations/${orgId}/reports/volume`, { format: "csv", days })} download>
                    Daily mail volume ({days}d)
                  </a>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      />

      <QueryView query={report} skeleton={<RowsSkeleton rows={6} />}>
        {(data) => (
          <div className="flex flex-col gap-8">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <SummaryCard
                label="Total storage"
                value={formatBytes(data.summary.totalStorageBytes)}
                detail={
                  data.summary.totalQuotaBytes > 0
                    ? `of ${formatBytes(data.summary.totalQuotaBytes)} quota · ${plural(data.summary.totalMessages, "message")}`
                    : `${plural(data.summary.totalMessages, "message")} across ${plural(data.summary.totalMailboxes, "mailbox")}`
                }
                icon={HardDrive}
              />
              <SummaryCard
                label={`Messages sent (${days}d)`}
                value={data.summary.sentTotal.toLocaleString()}
                detail={`${formatBytes(data.summary.sentBytesTotal)} total size`}
                icon={ArrowUpRight}
              />
              <SummaryCard
                label={`Messages received (${days}d)`}
                value={data.summary.receivedTotal.toLocaleString()}
                detail={`${formatBytes(data.summary.receivedBytesTotal)} total size`}
                icon={ArrowDownLeft}
              />
              <SummaryCard
                label={`Inactive mailboxes (${inactiveDays}d)`}
                value={data.summary.inactiveMailboxes.toLocaleString()}
                detail={`of ${plural(data.summary.activeMailboxes, "active mailbox")}`}
                icon={Clock}
                tone={data.summary.inactiveMailboxes > 0 ? "warning" : undefined}
              />
            </div>

            <VolumeChart series={data.volumeSeries} days={days} orgId={orgId} />

            <section className="flex flex-col gap-4">
              <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
                <div className="flex flex-col gap-1">
                  <h3 className="text-h3">Inactive mailboxes</h3>
                  <p className="text-sm text-fg-tertiary">
                    Active user mailboxes with no web, IMAP, or SMTP sign-in in the last {plural(inactiveDays, "day")}.
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-3">
                  <SegmentedControl
                    label="Inactive threshold presets"
                    options={inactivePresets}
                    value={inactiveDays}
                    onChange={(v) => setInactiveInput(String(v))}
                  />
                  <div className="flex items-center gap-2">
                    <Label htmlFor="inactive-days-input" className="text-xs whitespace-nowrap text-fg-tertiary">
                      Days
                    </Label>
                    <Input
                      id="inactive-days-input"
                      type="number"
                      min={1}
                      max={3650}
                      inputMode="numeric"
                      value={inactiveInput}
                      onChange={(e) => setInactiveInput(e.target.value)}
                      className="w-20 tabular-nums"
                    />
                  </div>
                  <Button variant="quiet" size="sm" asChild>
                    <a href={apiUrl(`/organizations/${orgId}/reports/inactive`, { format: "csv", inactiveDays })} download>
                      <Download aria-hidden />
                      CSV
                    </a>
                  </Button>
                </div>
              </div>

              {data.inactiveMailboxes.length === 0 ? (
                <div className="rounded-xl border border-border bg-card">
                  <EmptyState
                    icon={Clock}
                    title="No inactive mailboxes"
                    description={`Every active user mailbox signed in within the last ${plural(inactiveDays, "day")}.`}
                  />
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Mailbox</TableHead>
                      <TableHead>Last sign-in</TableHead>
                      <TableHead>Size and quota</TableHead>
                      <TableHead>Created</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {data.inactiveMailboxes.map((m) => (
                      <TableRow key={m.id}>
                        <TableCell>
                          <div className="flex flex-col gap-0.5">
                            <span className="font-mono text-sm">{m.address}</span>
                            {m.displayName && <span className="text-xs text-fg-tertiary">{m.displayName}</span>}
                          </div>
                        </TableCell>
                        <TableCell>
                          {m.lastSignInAt ? (
                            <LastSignIn
                              kind={m.kind}
                              lastSignInAt={m.lastSignInAt}
                              lastWebLoginAt={m.lastWebLoginAt}
                              lastImapLoginAt={m.lastImapLoginAt}
                              lastSmtpLoginAt={m.lastSmtpLoginAt}
                            />
                          ) : (
                            <StatusBadge tone="warning">Never signed in</StatusBadge>
                          )}
                        </TableCell>
                        <TableCell>
                          <QuotaUsage
                            storageBytes={m.storageBytes}
                            quotaBytes={m.quotaBytes}
                            messageCount={m.messageCount}
                            storageUpdatedAt={m.storageUpdatedAt}
                          />
                        </TableCell>
                        <TableCell className="text-sm text-fg-secondary tabular-nums">{formatDate(m.createdAt)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </section>

            <section className="flex flex-col gap-4">
              <div className="flex items-end justify-between gap-3">
                <div className="flex flex-col gap-1">
                  <h3 className="text-h3">Largest mailboxes</h3>
                  <p className="text-sm text-fg-tertiary">Mailboxes using the most storage in this organization.</p>
                </div>
                <Button variant="quiet" size="sm" asChild>
                  <a href={apiUrl(`/organizations/${orgId}/reports/largest`, { format: "csv" })} download>
                    <Download aria-hidden />
                    CSV
                  </a>
                </Button>
              </div>
              {data.largestMailboxes.length === 0 ? (
                <div className="rounded-xl border border-border bg-card">
                  <EmptyState icon={Inbox} title="No mailboxes" description="Create a mailbox to start tracking storage usage." />
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Mailbox</TableHead>
                      <TableHead>Size and quota</TableHead>
                      <TableHead className="text-right">Messages</TableHead>
                      <TableHead>Last sign-in</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {data.largestMailboxes.map((m) => (
                      <TableRow key={m.id}>
                        <TableCell>
                          <div className="flex flex-col gap-0.5">
                            <div className="flex items-center gap-2">
                              <span className="font-mono text-sm">{m.address}</span>
                              {m.kind === "shared" && <StatusBadge tone="neutral">Shared</StatusBadge>}
                              {!m.active && <StatusBadge tone="neutral">Inactive</StatusBadge>}
                            </div>
                            {m.displayName && <span className="text-xs text-fg-tertiary">{m.displayName}</span>}
                          </div>
                        </TableCell>
                        <TableCell>
                          <QuotaUsage
                            storageBytes={m.storageBytes}
                            quotaBytes={m.quotaBytes}
                            messageCount={m.messageCount}
                            storageUpdatedAt={m.storageUpdatedAt}
                          />
                        </TableCell>
                        <TableCell className="text-right tabular-nums">{m.messageCount.toLocaleString()}</TableCell>
                        <TableCell>
                          <LastSignIn
                            kind={m.kind}
                            lastSignInAt={m.lastSignInAt}
                            lastWebLoginAt={m.lastWebLoginAt}
                            lastImapLoginAt={m.lastImapLoginAt}
                            lastSmtpLoginAt={m.lastSmtpLoginAt}
                          />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </section>

            <div className="grid grid-cols-1 gap-8 xl:grid-cols-2">
              <ActivityTable
                title="Top senders"
                description={`Mailboxes that sent the most messages in the last ${days} days.`}
                items={data.topSenders}
                direction="sent"
                orgId={orgId}
                days={days}
              />
              <ActivityTable
                title="Top receivers"
                description={`Mailboxes that received the most messages in the last ${days} days.`}
                items={data.topReceivers}
                direction="received"
                orgId={orgId}
                days={days}
              />
            </div>
          </div>
        )}
      </QueryView>
    </section>
  )
}

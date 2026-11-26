"use client"

import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { BellOff, ChevronRight } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { AlertRules } from "@/components/admin/alert-rules"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { alertSeverities, alertStatuses } from "@/lib/alerts"
import { api } from "@/lib/api"
import { formatDateTime, formatListDate, formatMeasure, plural } from "@/lib/format"
import { alertRulesQuery, alertSummaryQuery } from "@/lib/queries"
import type { Alert, AlertList, AlertStatus } from "@/lib/types"

const PAGE = 50

type StatusFilter = "active" | "resolved" | "all"

const statusFilters: { value: StatusFilter; label: string }[] = [
  { value: "active", label: "Active" },
  { value: "resolved", label: "Resolved" },
  { value: "all", label: "All" },
]

const summaryOrder: AlertStatus[] = ["firing", "acknowledged", "resolved"]

function AlertBadges({ alert }: { alert: Alert }) {
  return (
    <>
      <StatusBadge tone={alertSeverities[alert.severity].tone}>{alertSeverities[alert.severity].label}</StatusBadge>
      <StatusBadge tone={alertStatuses[alert.status].tone}>{alertStatuses[alert.status].label}</StatusBadge>
    </>
  )
}

export function AlertsTab() {
  return (
    <div className="flex flex-col gap-12">
      <Alerts />
      <AlertRules />
    </div>
  )
}

function Alerts() {
  const summary = useQuery(alertSummaryQuery)
  const rules = useQuery(alertRulesQuery)
  const [status, setStatus] = useState<StatusFilter>("active")
  const [severity, setSeverity] = useState("any")
  const [rule, setRule] = useState("any")
  const [open, setOpen] = useState<Alert | null>(null)
  const list = useInfiniteQuery({
    queryKey: ["alerts", "list", status, severity, rule],
    queryFn: ({ pageParam }) =>
      api<AlertList>("/admin/alerts", {
        query: {
          status: status === "all" ? undefined : status,
          severity: severity === "any" ? undefined : severity,
          rule: rule === "any" ? undefined : rule,
          limit: PAGE,
          offset: pageParam,
        },
      }),
    initialPageParam: 0,
    getNextPageParam: (last, pages) => (last.alerts.length === PAGE ? pages.length * PAGE : undefined),
    placeholderData: keepPreviousData,
    refetchInterval: 60_000,
  })
  const alerts = list.data?.pages.flatMap((p) => p.alerts) ?? []
  const filtered = severity !== "any" || rule !== "any"

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader title="Alerts" description="What Aether found while checking mail flow, certificates, DNS, mailbox access and storage." />
      <dl className="grid grid-cols-3 gap-2 sm:gap-3">
        {summaryOrder.map((s) => (
          <div key={s} className="flex flex-col gap-1 rounded-xl border border-border bg-card p-3 sm:p-4">
            <dt className="text-label text-fg-tertiary">{alertStatuses[s].label}</dt>
            <dd className="text-h2 tabular-nums">
              {summary.data ? (
                <span className={cn(s === "firing" && summary.data.firing > 0 && "text-danger")}>{summary.data[s]}</span>
              ) : summary.isError ? (
                "—"
              ) : (
                <Skeleton className="h-8 w-12" />
              )}
            </dd>
          </div>
        ))}
      </dl>
      <div className="flex flex-wrap items-center gap-3">
        <SegmentedControl label="Status" options={statusFilters} value={status} onChange={setStatus} />
        <Select value={severity} onValueChange={setSeverity}>
          <SelectTrigger aria-label="Severity" className="min-w-32 flex-1 sm:min-w-40 sm:flex-none">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="any">Any severity</SelectItem>
            {Object.entries(alertSeverities).map(([value, { label }]) => (
              <SelectItem key={value} value={value}>
                {label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={rule} onValueChange={setRule}>
          <SelectTrigger aria-label="Rule" className="min-w-32 flex-1 sm:min-w-56 sm:flex-none">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="any">Any rule</SelectItem>
            {rules.data?.map((r) => (
              <SelectItem key={r.key} value={r.key}>
                {r.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {list.isPending ? (
        <RowsSkeleton rows={4} />
      ) : list.isError ? (
        <ErrorState error={list.error} onRetry={() => list.refetch()} />
      ) : alerts.length === 0 ? (
        <EmptyState
          icon={BellOff}
          title={status === "active" ? "No active alerts" : "No alerts to show"}
          description={
            filtered
              ? "No alert matches these filters."
              : status === "active"
                ? "Every rule Aether can check is within its threshold."
                : "Alerts appear here when a rule crosses its threshold."
          }
        />
      ) : (
        <div className={cn("animate-content flex flex-col gap-4 transition-opacity duration-160", list.isPlaceholderData && "opacity-60")}>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Alert</TableHead>
                <TableHead className="max-md:hidden">Severity</TableHead>
                <TableHead className="max-md:hidden">Status</TableHead>
                <TableHead className="text-right max-lg:hidden">Observed</TableHead>
                <TableHead className="max-md:hidden">Fired</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Details</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {alerts.map((a) => (
                <TableRow key={a.id} className="cursor-pointer" onClick={() => setOpen(a)}>
                  <TableCell className="whitespace-normal">
                    <div className="flex flex-col gap-1">
                      <span className="font-medium">{a.title}</span>
                      <span className="text-sm text-fg-tertiary">
                        {a.ruleName}
                        <span className="whitespace-nowrap md:hidden"> · {formatListDate(a.firedAt)}</span>
                      </span>
                      <div className="flex flex-wrap gap-2 pt-1 md:hidden">
                        <AlertBadges alert={a} />
                      </div>
                    </div>
                  </TableCell>
                  <TableCell className="max-md:hidden">
                    <StatusBadge tone={alertSeverities[a.severity].tone}>{alertSeverities[a.severity].label}</StatusBadge>
                  </TableCell>
                  <TableCell className="max-md:hidden">
                    <StatusBadge tone={alertStatuses[a.status].tone}>{alertStatuses[a.status].label}</StatusBadge>
                  </TableCell>
                  <TableCell className="text-right tabular-nums max-lg:hidden">{a.currentValue === null ? "—" : formatMeasure(a.currentValue, a.unit)}</TableCell>
                  <TableCell className="max-md:hidden">{formatListDate(a.firedAt)}</TableCell>
                  <TableCell>
                    <Button variant="quiet" size="icon-sm" aria-label={`Open ${a.title}`} onClick={() => setOpen(a)}>
                      <ChevronRight />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {list.hasNextPage && (
            <Button variant="secondary" className="self-center" loading={list.isFetchingNextPage} onClick={() => list.fetchNextPage()}>
              Load more alerts
            </Button>
          )}
        </div>
      )}

      {open && <AlertDialog key={open.id} initial={open} onClose={() => setOpen(null)} />}
    </section>
  )
}

function AlertDialog({ initial, onClose }: { initial: Alert; onClose: () => void }) {
  const qc = useQueryClient()
  const queryKey = ["alerts", "detail", initial.id]
  const alert = useQuery({ queryKey, queryFn: () => api<Alert>(`/admin/alerts/${initial.id}`), initialData: initial }).data

  const act = useMutation({
    mutationFn: (action: "acknowledge" | "resolve") => api<Alert>(`/admin/alerts/${initial.id}/${action}`, { method: "POST" }),
    onSuccess: (updated, action) => {
      qc.setQueryData(queryKey, updated)
      toast.success(action === "resolve" ? "Alert resolved" : "Alert acknowledged")
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ["alerts"] }),
  })

  const when = (iso: string, by: string | null) => `${formatDateTime(iso)}${by ? ` by ${by}` : ""}`
  const rows: [string, string | false | null][] = [
    ["Rule", alert.ruleName],
    ["Target", alert.targetLabel],
    ["Observed", alert.currentValue !== null && formatMeasure(alert.currentValue, alert.unit)],
    ["Threshold", alert.thresholdValue !== null && formatMeasure(alert.thresholdValue, alert.unit)],
    ["Fired", formatDateTime(alert.firedAt)],
    ["Last seen", `${formatDateTime(alert.lastSeenAt)} · ${plural(alert.occurrences, "check")}`],
    ["Last notified", alert.lastNotifiedAt ? formatDateTime(alert.lastNotifiedAt) : "No notification sent"],
    ["Acknowledged", alert.acknowledgedAt && when(alert.acknowledgedAt, alert.acknowledgedByEmail)],
    ["Resolved", alert.resolvedAt && when(alert.resolvedAt, alert.resolvedByEmail)],
  ]

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="grid-cols-1 sm:max-w-[800px]">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-3">
            <span>{alert.title}</span>
            <AlertBadges alert={alert} />
          </DialogTitle>
          <DialogDescription>{alert.message}</DialogDescription>
        </DialogHeader>
        <dl className="flex flex-col rounded-xl border border-border">
          {rows.map(
            ([label, value]) =>
              value && (
                <div key={label} className="flex flex-col gap-1 border-b border-border p-3 last:border-b-0 sm:flex-row sm:gap-4">
                  <dt className="w-28 shrink-0 text-label text-fg-tertiary">{label}</dt>
                  <dd className="min-w-0 text-sm break-words tabular-nums">{value}</dd>
                </div>
              )
          )}
        </dl>
        {Object.keys(alert.details).length > 0 && (
          <div className="flex flex-col gap-2">
            <span className="text-label text-fg-secondary">Details</span>
            <pre className="max-h-64 overflow-auto rounded-xl border border-border bg-sunken p-4 font-mono text-sm">{JSON.stringify(alert.details, null, 2)}</pre>
          </div>
        )}
        {alert.status !== "resolved" && (
          <>
            <p className="text-sm text-fg-tertiary">
              Acknowledging stops further notifications until the alert resolves. Resolving closes it now; if the problem is still there at the next check, Aether opens a new alert.
            </p>
            <DialogFooter>
              {alert.status === "firing" && (
                <Button variant="secondary" disabled={act.isPending} loading={act.isPending && act.variables === "acknowledge"} onClick={() => act.mutate("acknowledge")}>
                  Acknowledge
                </Button>
              )}
              <Button disabled={act.isPending} loading={act.isPending && act.variables === "resolve"} onClick={() => act.mutate("resolve")}>
                Resolve
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

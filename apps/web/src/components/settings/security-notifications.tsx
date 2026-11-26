"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  Check,
  CheckCheck,
  ExternalLink,
  Flag,
  Forward,
  KeyRound,
  MonitorSmartphone,
  ShieldAlert,
  ShieldCheck,
  Undo2,
  UserPlus,
  type LucideIcon,
} from "lucide-react"
import { useEffect, useState, useSyncExternalStore } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/input"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import type { SecurityEventType, SecurityNotification, SecurityNotificationsResponse } from "@/lib/types"

const eventMeta: Record<SecurityEventType, { icon: LucideIcon; tone: string }> = {
  app_password_created: { icon: KeyRound, tone: "bg-info/12 text-info" },
  external_forwarding_rule: { icon: Forward, tone: "bg-warning/12 text-warning" },
  delegate_added: { icon: UserPlus, tone: "bg-info/12 text-info" },
  new_device_sign_in: { icon: MonitorSmartphone, tone: "bg-warning/12 text-warning" },
  failed_sign_ins: { icon: ShieldAlert, tone: "bg-danger/12 text-danger" },
}

function undoDescription(n: SecurityNotification) {
  switch (n.eventType) {
    case "app_password_created":
      return "Revokes the app password immediately so mail, calendar, and contacts clients using it stop connecting."
    case "external_forwarding_rule":
      return "Removes the external forwarding address from your mail rules and updates the mail server right away."
    case "delegate_added":
      return "Removes this person's delegate access to your mailbox and ends their active mail sessions."
    case "new_device_sign_in":
      return "Signs out the session created by this sign-in immediately."
    default:
      return "Undoes the change recorded in this alert."
  }
}

function reviewLabel(n: SecurityNotification) {
  switch (n.eventType) {
    case "app_password_created":
      return "App passwords"
    case "external_forwarding_rule":
      return "Mail rules"
    default:
      return ""
  }
}

function detailChips(n: SecurityNotification) {
  const out: { label: string; value: string; mono?: boolean }[] = []
  const d = n.details
  if (n.mailboxAddress) out.push({ label: "Mailbox", value: n.mailboxAddress, mono: true })
  if (typeof d.appPasswordName === "string" && d.appPasswordName) out.push({ label: "App password", value: d.appPasswordName })
  if (typeof d.delegateEmail === "string" && d.delegateEmail) {
    const access = typeof d.access === "string" && d.access ? ` (${d.access})` : ""
    out.push({ label: "Delegate", value: `${d.delegateEmail}${access}`, mono: true })
  }
  if (Array.isArray(d.addresses) && d.addresses.length) {
    out.push({ label: "Forwarding to", value: d.addresses.join(", "), mono: true })
  }
  if (Array.isArray(d.ruleNames) && d.ruleNames.length) {
    out.push({ label: "Rule", value: d.ruleNames.join(", ") })
  }
  if (typeof d.device === "string" && d.device) out.push({ label: "Device", value: d.device })
  if (typeof d.country === "string" && d.country) out.push({ label: "Country", value: d.country })
  if (typeof d.ip === "string" && d.ip) out.push({ label: "IP", value: d.ip, mono: true })
  if (typeof d.actorLabel === "string" && d.actorLabel) out.push({ label: "By", value: d.actorLabel })
  return out
}

function subscribeLocation(cb: () => void) {
  window.addEventListener("popstate", cb)
  return () => window.removeEventListener("popstate", cb)
}

function getAlertParam() {
  return new URLSearchParams(window.location.search).get("alert")
}

function getServerAlertParam() {
  return null
}

export function SecurityNotifications() {
  const qc = useQueryClient()
  const highlightedId = useSyncExternalStore(subscribeLocation, getAlertParam, getServerAlertParam)
  const [undoing, setUndoing] = useState<SecurityNotification | null>(null)
  const [reporting, setReporting] = useState<SecurityNotification | null>(null)
  const [reportNote, setReportNote] = useState("")

  const query = useQuery({
    queryKey: ["security-notifications"],
    queryFn: () => api<SecurityNotificationsResponse>("/me/security-notifications"),
  })

  useEffect(() => {
    if (!highlightedId || !query.data) return
    const el = document.getElementById(`security-alert-${highlightedId}`)
    if (el) el.scrollIntoView({ behavior: "smooth", block: "center" })
  }, [highlightedId, query.data])

  const markRead = useMutation({
    mutationFn: (id: string) => api<SecurityNotification>(`/me/security-notifications/${id}/read`, { method: "POST" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["security-notifications"] }),
  })

  const markAllRead = useMutation({
    mutationFn: () => api("/me/security-notifications/read-all", { method: "POST" }),
    onSuccess: () => {
      toast.success("All security alerts marked as read")
      qc.invalidateQueries({ queryKey: ["security-notifications"] })
    },
  })

  const undo = useMutation({
    mutationFn: (id: string) =>
      api<{ notification: SecurityNotification; message: string }>(`/me/security-notifications/${id}/undo`, { method: "POST" }),
    onSuccess: (res) => {
      toast.success(res.message || "Change undone")
      setUndoing(null)
      qc.invalidateQueries({ queryKey: ["security-notifications"] })
      qc.invalidateQueries({ queryKey: ["app-passwords"] })
      qc.invalidateQueries({ queryKey: ["rules"] })
    },
  })

  const report = useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) =>
      api<SecurityNotification>(`/me/security-notifications/${id}/report`, {
        method: "POST",
        body: note.trim() ? { note: note.trim() } : {},
      }),
    onSuccess: () => {
      toast.success("Reported to your organization’s administrators")
      setReporting(null)
      setReportNote("")
      qc.invalidateQueries({ queryKey: ["security-notifications"] })
    },
  })

  const notifications = query.data?.notifications ?? []
  const unreadCount = query.data?.unreadCount ?? 0

  return (
    <section id="security" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader
        title="Security"
        description="Recent security alerts for your account and mailboxes. Review each event, undo changes you did not make, or report suspicious activity to your organization’s administrators."
        actions={
          unreadCount > 0 ? (
            <div className="flex items-center gap-3">
              <StatusBadge tone="warning">{unreadCount} unread</StatusBadge>
              <Button variant="secondary" size="sm" loading={markAllRead.isPending} onClick={() => markAllRead.mutate()}>
                <CheckCheck aria-hidden />
                Mark all read
              </Button>
            </div>
          ) : undefined
        }
      />

      {query.isPending ? (
        <RowsSkeleton rows={3} />
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => query.refetch()} />
      ) : notifications.length === 0 ? (
        <EmptyState
          icon={ShieldCheck}
          title="No security alerts"
          description="You will see a notice here if an app password is created, an external forwarding rule is added, delegate access is granted, or unusual sign-in activity occurs."
        />
      ) : (
        <div className="animate-content divide-y divide-border rounded-xl border border-border bg-card">
          {notifications.map((n) => {
            const meta = eventMeta[n.eventType] ?? { icon: ShieldAlert, tone: "bg-muted text-fg-secondary" }
            const Icon = meta.icon
            const unread = !n.readAt
            const highlighted = highlightedId === n.id
            const chips = detailChips(n)
            const linkLabel = reviewLabel(n)
            const note = typeof n.details.reportNote === "string" ? n.details.reportNote : ""

            return (
              <article
                key={n.id}
                id={`security-alert-${n.id}`}
                className={cn(
                  "flex flex-col gap-3 p-4 transition-colors sm:p-5",
                  unread && "bg-ember/4",
                  highlighted && "ring-2 ring-inset ring-ember"
                )}
              >
                <div className="flex items-start gap-3">
                  <div className={cn("mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-lg", meta.tone)}>
                    <Icon aria-hidden className="size-4" />
                  </div>
                  <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex flex-wrap items-center gap-2">
                        {unread && <span className="size-2 shrink-0 rounded-full bg-ember" aria-label="Unread alert" />}
                        <h3 className="text-[15px] font-semibold text-foreground">{n.title}</h3>
                        {n.undoneAt && <StatusBadge tone="neutral">Undone</StatusBadge>}
                        {n.reportedAt && <StatusBadge tone="warning">Reported</StatusBadge>}
                      </div>
                      <time dateTime={n.createdAt} className="text-xs text-fg-tertiary">
                        {formatDateTime(n.createdAt)}
                      </time>
                    </div>
                    <p className="text-sm text-fg-secondary">{n.summary}</p>
                    {chips.length > 0 && (
                      <dl className="flex flex-wrap gap-x-4 gap-y-1 pt-0.5 text-xs text-fg-tertiary">
                        {chips.map((c) => (
                          <div key={c.label} className="flex items-baseline gap-1.5">
                            <dt className="font-medium text-fg-tertiary">{c.label}:</dt>
                            <dd className={cn("text-fg-secondary break-all", c.mono && "font-mono")}>{c.value}</dd>
                          </div>
                        ))}
                      </dl>
                    )}
                    {note && (
                      <p className="rounded-lg bg-muted/60 px-3 py-2 text-xs text-fg-secondary">
                        <span className="font-medium text-foreground">Your report note:</span> {note}
                      </p>
                    )}
                    <div className="flex flex-wrap items-center gap-2 pt-1">
                      {n.canUndo && !n.undoneAt && (
                        <Button variant="destructive" size="sm" onClick={() => setUndoing(n)}>
                          <Undo2 aria-hidden />
                          {n.undoLabel || "Undo"}
                        </Button>
                      )}
                      {linkLabel && n.actionUrl && (
                        <Button variant="secondary" size="sm" asChild>
                          <a href={n.actionUrl}>
                            <ExternalLink aria-hidden />
                            Review {linkLabel.toLowerCase()}
                          </a>
                        </Button>
                      )}
                      {!n.reportedAt && (
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => {
                            setReportNote("")
                            setReporting(n)
                          }}
                        >
                          <Flag aria-hidden />
                          Report
                        </Button>
                      )}
                      {unread && (
                        <Button
                          variant="quiet"
                          size="sm"
                          loading={markRead.isPending && markRead.variables === n.id}
                          onClick={() => markRead.mutate(n.id)}
                        >
                          <Check aria-hidden />
                          Mark read
                        </Button>
                      )}
                    </div>
                  </div>
                </div>
              </article>
            )
          })}
        </div>
      )}

      <ConfirmDialog
        open={!!undoing}
        onOpenChange={(open) => !open && setUndoing(null)}
        title={undoing ? `${undoing.undoLabel}?` : "Undo action?"}
        description={undoing ? undoDescription(undoing) : ""}
        confirmLabel={undoing?.undoLabel || "Undo"}
        pending={undo.isPending}
        onConfirm={() => undoing && undo.mutate(undoing.id)}
      />

      <FormDialog
        open={!!reporting}
        onOpenChange={(open) => {
          if (!open) {
            setReporting(null)
            setReportNote("")
          }
        }}
        title="Report suspicious activity"
        description="Your organization’s administrators will be notified by email and the report will be recorded in the audit log."
        submitLabel="Send report"
        pending={report.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (reporting) report.mutate({ id: reporting.id, note: reportNote })
        }}
      >
        {reporting && (
          <div className="rounded-lg border border-border bg-muted/40 p-3 text-sm">
            <p className="font-semibold text-foreground">{reporting.title}</p>
            <p className="mt-1 text-fg-secondary">{reporting.summary}</p>
          </div>
        )}
        <Field id="security-report-note" label="Details (optional)" hint="Add anything that can help your administrator investigate, such as your location or what you were doing.">
          <Textarea
            id="security-report-note"
            rows={3}
            maxLength={1000}
            value={reportNote}
            onChange={(e) => setReportNote(e.target.value)}
            placeholder="I did not authorize this change…"
          />
        </Field>
      </FormDialog>
    </section>
  )
}

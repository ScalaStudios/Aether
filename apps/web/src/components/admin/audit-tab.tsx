"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { CalendarX, ChevronRight, Clock, Download, ScrollText, SearchX, TriangleAlert } from "lucide-react"
import { useSearchParams } from "next/navigation"
import { Fragment, useEffect, useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"
import { cn, setQueryParams } from "@/lib/utils"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { SearchInput } from "@/components/search-input"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, ErrorState, QueryView, RowsSkeleton } from "@/components/states"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { api, apiUrl } from "@/lib/api"
import { auditActionLabel, auditActor, auditCategories, auditDetail, auditTargetLabel, auditTargetName, platformAuditCategory } from "@/lib/audit"
import { formatDateTime, plural } from "@/lib/format"
import { organizationsQuery } from "@/lib/queries"
import type { AuditEvent, AuditRetention, MailboxAuditSettings } from "@/lib/types"

const PAGE = 50
const MAX_RETENTION_DAYS = 3650

const emptyDraft = { q: "", actor: "", target: "" }

const keptFor = (days: number) => (days ? `for ${plural(days, "day")}` : "forever")

function dayBound(day: string, end: boolean) {
  const date = new Date(`${day}T${end ? "23:59:59.999" : "00:00:00"}`)
  return /^\d{4}-\d{2}-\d{2}$/.test(day) && !Number.isNaN(date.getTime()) ? date.toISOString() : undefined
}

export function AuditTab({ orgId }: { orgId?: string }) {
  if (!orgId) return <AuditLog />
  return (
    <div className="flex flex-col gap-10">
      <MailboxAuditSection orgId={orgId} />
      <AuditLog orgId={orgId} />
    </div>
  )
}

function AuditLog({ orgId }: { orgId?: string }) {
  const params = useSearchParams()
  const organizations = useQuery(organizationsQuery).data ?? []
  const [draft, setDraft] = useState(() => ({ q: params.get("q") ?? "", actor: params.get("actor") ?? "", target: params.get("target") ?? "" }))
  const settled = useDebouncedValue(draft, 300)
  const [openId, setOpenId] = useState<number | null>(null)
  const [editingRetention, setEditingRetention] = useState(false)

  useEffect(() => {
    setQueryParams({ q: settled.q.trim() || null, actor: settled.actor.trim() || null, target: settled.target.trim() || null })
  }, [settled])

  const since = params.get("since") ?? ""
  const until = params.get("until") ?? ""
  const action = params.get("action") ?? ""
  const scope = params.get("scope") ?? ""
  const inverted = !!since && !!until && since > until
  const query = {
    q: params.get("q") ?? "",
    actor: params.get("actor") ?? "",
    target: params.get("target") ?? "",
    action,
    since: dayBound(since, false),
    until: dayBound(until, true),
    orgId: orgId ? undefined : scope,
  }
  const filtered = Object.values(query).some(Boolean) || Object.values(draft).some(Boolean)
  const path = orgId ? `/organizations/${orgId}/audit` : "/admin/audit"
  const categories = orgId ? auditCategories : [...auditCategories, platformAuditCategory]
  const orgName = (id: string | null) => (id ? (organizations.find((o) => o.id === id)?.name ?? id) : "—")

  const audit = useInfiniteQuery({
    queryKey: ["audit", orgId ?? "platform", query],
    queryFn: ({ pageParam }) => api<AuditEvent[]>(path, { query: { ...query, before: pageParam } }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => (last.length === PAGE ? last[last.length - 1]?.id : undefined),
    placeholderData: keepPreviousData,
    enabled: !inverted,
  })
  const retention = useQuery({
    queryKey: ["audit-retention", orgId],
    queryFn: () => api<AuditRetention>(`/organizations/${orgId}/audit/retention`),
    enabled: !!orgId,
  })
  const events = audit.data?.pages.flat() ?? []

  function clear() {
    setDraft(emptyDraft)
    setQueryParams({ q: null, actor: null, target: null, action: null, since: null, until: null, scope: null })
  }

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title={orgId ? "Audit log" : "Platform audit log"}
        description={
          orgId
            ? `Changes and sign-ins in this organization, newest first.${retention.data ? ` Events are kept ${keptFor(retention.data.effectiveRetentionDays)}.` : ""}`
            : "Events from every organization and those that belong to none, newest first."
        }
        actions={
          <>
            {orgId && (
              <Button variant="secondary" disabled={!retention.data} onClick={() => setEditingRetention(true)}>
                <Clock aria-hidden />
                Retention
              </Button>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="secondary" disabled={inverted}>
                  <Download aria-hidden />
                  Export
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-72">
                <DropdownMenuLabel className="px-3 py-2">{filtered ? "Events matching the filters" : "All events"}, up to 50,000</DropdownMenuLabel>
                {(["csv", "json"] as const).map((format) => (
                  <DropdownMenuItem key={format} asChild>
                    <a href={apiUrl(`${path}/export`, { ...query, format })} download>
                      Download {format.toUpperCase()}
                    </a>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      />

      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-3">
          <SearchInput
            aria-label="Search the audit log"
            placeholder="Search actions, addresses and details"
            value={draft.q}
            onChange={(e) => setDraft({ ...draft, q: e.target.value })}
            className="min-w-0 flex-1 basis-64 md:max-w-md"
          />
          {filtered && (
            <Button variant="quiet" size="sm" onClick={clear}>
              Clear filters
            </Button>
          )}
        </div>
        <div className={cn("grid grid-cols-2 gap-4 md:grid-cols-3", orgId ? "xl:grid-cols-5" : "xl:grid-cols-6")}>
          {!orgId && (
            <div className="col-span-2 md:col-span-1">
              <Field id="audit-scope" label="Belongs to">
                <Select value={scope || "all"} onValueChange={(v) => setQueryParams({ scope: v === "all" ? null : v })}>
                  <SelectTrigger id="audit-scope" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">All events</SelectItem>
                    <SelectItem value="none">No organization</SelectItem>
                    {organizations.map((o) => (
                      <SelectItem key={o.id} value={o.id}>
                        {o.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
          )}
          <div className="col-span-2 md:col-span-1">
            <Field id="audit-action" label="Action">
              <Select value={action || "all"} onValueChange={(v) => setQueryParams({ action: v === "all" ? null : v })}>
                <SelectTrigger id="audit-action" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All actions</SelectItem>
                  {categories.map((c) => (
                    <SelectItem key={c.value} value={c.value}>
                      {c.label}
                    </SelectItem>
                  ))}
                  {action && !categories.some((c) => c.value === action) && <SelectItem value={action}>{action}</SelectItem>}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field id="audit-actor" label="Actor">
            <Input id="audit-actor" placeholder="Email or API key" autoComplete="off" spellCheck={false} value={draft.actor} onChange={(e) => setDraft({ ...draft, actor: e.target.value })} />
          </Field>
          <Field id="audit-target" label="Target">
            <Input id="audit-target" placeholder="Address, type or ID" autoComplete="off" spellCheck={false} value={draft.target} onChange={(e) => setDraft({ ...draft, target: e.target.value })} />
          </Field>
          <Field id="audit-since" label="From">
            <Input id="audit-since" type="date" max={until || undefined} value={since} onChange={(e) => setQueryParams({ since: e.target.value || null })} />
          </Field>
          <Field id="audit-until" label="To" error={inverted ? "Choose the start date or later." : undefined}>
            <Input id="audit-until" type="date" min={since || undefined} aria-invalid={inverted} value={until} onChange={(e) => setQueryParams({ until: e.target.value || null })} />
          </Field>
        </div>
      </div>

      {inverted ? (
        <EmptyState icon={CalendarX} title="Check the date range" description="The start date is after the end date." />
      ) : audit.isPending ? (
        <RowsSkeleton rows={5} />
      ) : audit.isError ? (
        <ErrorState error={audit.error} onRetry={() => audit.refetch()} />
      ) : events.length === 0 ? (
        filtered ? (
          <EmptyState
            icon={SearchX}
            title="No events match"
            description="Change or clear the filters to see more events."
            action={
              <Button variant="secondary" onClick={clear}>
                Clear filters
              </Button>
            }
          />
        ) : (
          <EmptyState icon={ScrollText} title="No events yet" description="Actions such as creating mailboxes or verifying domains are recorded here." />
        )
      ) : (
        <div aria-busy={audit.isPlaceholderData} className={cn("animate-content @container flex flex-col gap-4 transition-opacity duration-160", audit.isPlaceholderData && "opacity-60")}>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-12">
                  <span className="sr-only">Details</span>
                </TableHead>
                <TableHead>Time</TableHead>
                {!orgId && <TableHead>Organization</TableHead>}
                <TableHead>Actor</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Target</TableHead>
                <TableHead>IP address</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((e) => {
                const open = openId === e.id
                const actor = auditActor(e)
                const who = (
                  <>
                    {actor.name}
                    {actor.note && <span className="text-fg-tertiary"> · {actor.note}</span>}
                  </>
                )
                const details = Object.entries(e.metadata)
                return (
                  <Fragment key={e.id}>
                    <TableRow className="cursor-pointer" onClick={() => window.getSelection()?.toString() || setOpenId(open ? null : e.id)}>
                      <TableCell className="pr-0">
                        <Button variant="quiet" size="icon-sm" aria-expanded={open} aria-label={`Details of event ${e.id}`}>
                          <ChevronRight className={cn("transition-transform duration-160", open && "rotate-90")} />
                        </Button>
                      </TableCell>
                      <TableCell>{formatDateTime(e.createdAt)}</TableCell>
                      {!orgId && <TableCell className="max-w-40 truncate">{orgName(e.orgId)}</TableCell>}
                      <TableCell className={cn("truncate", orgId ? "max-w-56" : "max-w-48")}>{who}</TableCell>
                      <TableCell>{auditActionLabel(e.action)}</TableCell>
                      <TableCell className={cn("truncate", orgId ? "max-w-64" : "max-w-56")}>
                        <span className="text-fg-tertiary">{auditTargetLabel(e.targetType)}</span> {e.targetType === "organization" ? orgName(e.targetId) : auditTargetName(e)}
                      </TableCell>
                      <TableCell className="max-w-40 truncate font-mono text-sm">{e.ip || "—"}</TableCell>
                    </TableRow>
                    {open && (
                      <TableRow className="bg-sunken hover:bg-sunken">
                        <TableCell colSpan={orgId ? 6 : 7} className="p-0">
                          <div className="sticky left-0 grid w-[calc(100cqw-2px)] gap-6 px-4 py-6 whitespace-normal md:grid-cols-2 md:px-16">
                            <dl className="grid content-start gap-x-6 gap-y-2 text-sm sm:grid-cols-[8rem_minmax(0,1fr)]">
                              <dt className="text-fg-tertiary">Action</dt>
                              <dd className="font-mono break-all">{e.action}</dd>
                              <dt className="text-fg-tertiary">Time</dt>
                              <dd className="font-mono break-all">{e.createdAt}</dd>
                              <dt className="text-fg-tertiary">Actor</dt>
                              <dd className="break-all">
                                {who}
                                {e.actorUserId && <span className="block font-mono text-fg-tertiary">{e.actorUserId}</span>}
                              </dd>
                              <dt className="text-fg-tertiary">Target</dt>
                              <dd className="font-mono break-all">
                                {e.targetType} {e.targetId}
                              </dd>
                              {!orgId && (
                                <>
                                  <dt className="text-fg-tertiary">Organization</dt>
                                  <dd className="break-all">
                                    {orgName(e.orgId)}
                                    {e.orgId && <span className="block font-mono text-fg-tertiary">{e.orgId}</span>}
                                  </dd>
                                </>
                              )}
                              <dt className="text-fg-tertiary">IP address</dt>
                              <dd className="font-mono break-all">{e.ip || "—"}</dd>
                              <dt className="text-fg-tertiary">Event ID</dt>
                              <dd className="font-mono">{e.id}</dd>
                            </dl>
                            {details.length === 0 ? (
                              <p className="text-sm text-fg-tertiary">This event has no further details.</p>
                            ) : (
                              <dl className="grid content-start gap-x-6 gap-y-2 text-sm sm:grid-cols-[8rem_minmax(0,1fr)]">
                                {details.map(([key, value]) => (
                                  <Fragment key={key}>
                                    <dt className="font-mono break-all text-fg-tertiary">{key}</dt>
                                    <dd className="font-mono break-all whitespace-pre-wrap">{auditDetail(value)}</dd>
                                  </Fragment>
                                ))}
                              </dl>
                            )}
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                )
              })}
            </TableBody>
          </Table>
          {audit.hasNextPage && (
            <Button variant="secondary" className="self-center" loading={audit.isFetchingNextPage} onClick={() => audit.fetchNextPage()}>
              Load older events
            </Button>
          )}
        </div>
      )}

      {orgId && editingRetention && retention.data && <RetentionDialog orgId={orgId} retention={retention.data} onClose={() => setEditingRetention(false)} />}
    </section>
  )
}

const retentionSchema = z
  .object({ mode: z.enum(["default", "custom", "forever"]), days: z.string() })
  .refine((v) => v.mode !== "custom" || (/^\d+$/.test(v.days) && Number(v.days) >= 1 && Number(v.days) <= MAX_RETENTION_DAYS), {
    message: "Enter a number of days from 1 to 3,650.",
    path: ["days"],
  })

type RetentionValues = z.infer<typeof retentionSchema>

const retentionModes: { value: RetentionValues["mode"]; label: string }[] = [
  { value: "default", label: "Platform default" },
  { value: "custom", label: "Custom" },
  { value: "forever", label: "Forever" },
]

function RetentionDialog({ orgId, retention, onClose }: { orgId: string; retention: AuditRetention; onClose: () => void }) {
  const qc = useQueryClient()
  const form = useForm<RetentionValues>({
    resolver: zodResolver(retentionSchema),
    defaultValues: {
      mode: retention.retentionDays === null ? "default" : retention.retentionDays === 0 ? "forever" : "custom",
      days: retention.retentionDays ? String(retention.retentionDays) : "",
    },
  })
  const mode = useWatch({ control: form.control, name: "mode" })
  const days = useWatch({ control: form.control, name: "days" })
  const next = mode === "default" ? retention.defaultRetentionDays : mode === "forever" ? 0 : Number(days) || 0
  const deletes = next > 0 && (retention.effectiveRetentionDays === 0 || next < retention.effectiveRetentionDays)

  const save = useMutation({
    mutationFn: (v: RetentionValues) =>
      api<AuditRetention>(`/organizations/${orgId}/audit/retention`, {
        method: "PUT",
        body: { retentionDays: v.mode === "default" ? null : v.mode === "forever" ? 0 : Number(v.days) },
      }),
    onSuccess: (data) => {
      toast.success(`Audit events are now kept ${keptFor(data.effectiveRetentionDays)}`)
      qc.setQueryData(["audit-retention", orgId], data)
      qc.invalidateQueries({ queryKey: ["audit", orgId] })
      onClose()
    },
  })

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title="Audit log retention"
      description={`This organization's events are kept ${keptFor(retention.effectiveRetentionDays)}.`}
      submitLabel="Save retention"
      pending={save.isPending}
      onSubmit={form.handleSubmit((v) => save.mutate(v))}
    >
      <fieldset className="flex flex-col items-start">
        <legend className="mb-2 text-fg-secondary">Keep events</legend>
        <Controller control={form.control} name="mode" render={({ field }) => <SegmentedControl label="Keep events" options={retentionModes} value={field.value} onChange={field.onChange} />} />
      </fieldset>
      {mode === "custom" && (
        <Field id="retention-days" label="Days" hint="From 1 to 3,650." error={form.formState.errors.days?.message}>
          <Input id="retention-days" type="number" inputMode="numeric" min={1} max={MAX_RETENTION_DAYS} className="w-28" aria-invalid={!!form.formState.errors.days} {...form.register("days")} />
        </Field>
      )}
      <p className="text-sm text-fg-secondary">
        {mode === "default" ? `The platform default keeps events ${keptFor(retention.defaultRetentionDays)}.` : mode === "forever" ? "Events are never deleted." : "Aether deletes events once they are older than this."}
      </p>
      {deletes && (
        <p role="alert" className="flex items-start gap-2 text-sm text-warning">
          <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
          Events older than {plural(next, "day")} are deleted within an hour of saving and cannot be recovered.
        </p>
      )}
    </FormDialog>
  )
}

function MailboxAuditSection({ orgId }: { orgId: string }) {
  const settings = useQuery({ queryKey: ["mailbox-audit", orgId], queryFn: () => api<MailboxAuditSettings>(`/organizations/${orgId}/mailbox-audit`) })
  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Mailbox access"
        description="Record when delegates, shared mailbox members and administrators open, read, move, delete or send from a mailbox that is not theirs. Open a mailbox's access report from the Mailboxes tab."
      />
      <QueryView query={settings} skeleton={<RowsSkeleton rows={3} />}>
        {(data) => <MailboxAuditForm orgId={orgId} settings={data} />}
      </QueryView>
    </section>
  )
}

function MailboxAuditForm({ orgId, settings }: { orgId: string; settings: MailboxAuditSettings }) {
  const qc = useQueryClient()
  const form = useForm<MailboxAuditSettings>({ defaultValues: settings })
  const save = useMutation({
    mutationFn: (values: MailboxAuditSettings) => api<MailboxAuditSettings>(`/organizations/${orgId}/mailbox-audit`, { method: "PUT", body: values }),
    onSuccess: (saved) => {
      qc.setQueryData(["mailbox-audit", orgId], saved)
      form.reset(saved)
      toast.success("Mailbox access settings saved")
    },
  })
  const retention = form.formState.errors.retentionDays?.message

  return (
    <form className="flex flex-col gap-6 rounded-xl border border-border bg-card p-6" onSubmit={form.handleSubmit((v) => save.mutate(v))}>
      <SwitchField control={form.control} name="enabled" id="audit-enabled" label="Record mailbox access" hint="Covers Aether Mail and mail apps that connect over IMAP." />
      <SwitchField control={form.control} name="logReads" id="audit-reads" label="Include opened folders and read messages" hint="Mail apps that download every message record a read for each one." />
      <SwitchField control={form.control} name="logOwnerDeletes" id="audit-owner-deletes" label="Include deletions by the owner" hint="Applies to mail app sessions that start after the change." />
      <SwitchField control={form.control} name="ownersCanView" id="audit-owners" label="Let owners see who accessed their mailbox" hint="Owners find the report in Settings." />
      <Field id="audit-retention" label="Keep entries for (days)" error={retention}>
        <Input
          id="audit-retention"
          type="number"
          inputMode="numeric"
          min={1}
          max={3650}
          className="max-w-40"
          aria-invalid={!!retention}
          {...form.register("retentionDays", {
            valueAsNumber: true,
            validate: (v) => (Number.isInteger(v) && v >= 1 && v <= 3650) || "Enter a whole number of days from 1 to 3650.",
          })}
        />
      </Field>
      <Button type="submit" className="self-start" loading={save.isPending} disabled={!form.formState.isDirty}>
        Save
      </Button>
    </form>
  )
}

"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, Download, Lock, Pencil, Search, ShieldOff } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { CaseFields, caseSchema, type CaseValues } from "@/components/compliance/cases-tab"
import { SelectionList } from "@/components/compliance/selection-list"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, apiUrl } from "@/lib/api"
import { formatAddress, formatBytes, formatDate, formatDateTime, plural } from "@/lib/format"
import { caseQuery, complianceDirectoryQuery, holdsQuery } from "@/lib/queries"
import type { CaseCriteria, CaseScope, CaseSearchResult, ComplianceCase, ComplianceDirectory, Hold } from "@/lib/types"

const term = z.string().trim().max(500, "Use at most 500 characters.")

const criteriaSchema = z
  .object({
    scope: z.enum(["mailboxes", "domains", "organization"]),
    mailboxIds: z.array(z.string()),
    domainIds: z.array(z.string()),
    from: term,
    to: term,
    subject: term,
    body: term,
    attachment: term,
    since: z.string(),
    until: z.string(),
  })
  .superRefine((v, ctx) => {
    if (v.scope === "mailboxes" && v.mailboxIds.length === 0) ctx.addIssue({ code: "custom", path: ["mailboxIds"], message: "Choose at least one mailbox." })
    if (v.scope === "domains" && v.domainIds.length === 0) ctx.addIssue({ code: "custom", path: ["domainIds"], message: "Choose at least one domain." })
    if (v.since && v.until && v.until < v.since) ctx.addIssue({ code: "custom", path: ["until"], message: "The end date is before the start date." })
  })

const scopes: { value: CaseScope; label: string }[] = [
  { value: "mailboxes", label: "Mailboxes" },
  { value: "domains", label: "Domains" },
  { value: "organization", label: "Whole organization" },
]

const textFields: { name: "from" | "to" | "subject" | "body" | "attachment"; label: string; hint: string }[] = [
  { name: "from", label: "From", hint: "Part of the sender’s address or name." },
  { name: "to", label: "To, Cc or Bcc", hint: "Part of any recipient’s address or name." },
  { name: "subject", label: "Subject", hint: "Words in the subject." },
  { name: "body", label: "Body", hint: "Words in the message text." },
  { name: "attachment", label: "Attachment name", hint: "Part of an attachment’s file name." },
]

const termLabels: [keyof CaseCriteria, string][] = [
  ["from", "from"],
  ["to", "to"],
  ["subject", "subject"],
  ["body", "body"],
  ["attachment", "attachment"],
  ["since", "received from"],
  ["until", "received until"],
]

function describeTerms(c: CaseCriteria) {
  return termLabels
    .filter(([key]) => c[key])
    .map(([key, label]) => `${label} “${c[key]}”`)
    .join(", ")
}

function mailboxState(m: { active: boolean; retainedAt: string | null }) {
  if (m.retainedAt) return <StatusBadge tone="warning">Retained</StatusBadge>
  return <StatusBadge tone={m.active ? "success" : "neutral"}>{m.active ? "Active" : "Inactive"}</StatusBadge>
}

export function CaseDetail({ caseId, orgId, onBack }: { caseId: string; orgId: string; onBack: () => void }) {
  const qc = useQueryClient()
  const current = useQuery(caseQuery(caseId))
  const directory = useQuery(complianceDirectoryQuery(orgId))
  const [editing, setEditing] = useState(false)
  const [closing, setClosing] = useState(false)
  const form = useForm<CaseValues>({ resolver: zodResolver(caseSchema), defaultValues: { name: "", description: "" } })

  function refresh(c: ComplianceCase) {
    qc.setQueryData(caseQuery(caseId).queryKey, c)
    qc.invalidateQueries({ queryKey: ["compliance-cases", orgId] })
    qc.invalidateQueries({ queryKey: ["compliance-holds", caseId] })
    qc.invalidateQueries({ queryKey: ["compliance-directory", orgId] })
  }

  const update = useMutation({
    mutationFn: (body: Partial<CaseValues> & { closed?: boolean }) => api<ComplianceCase>(`/compliance/cases/${caseId}`, { method: "PATCH", body }),
    onSuccess: (c, body) => {
      refresh(c)
      if (body.closed === true) toast.success(`Closed ${c.name}`, { description: "Its holds were released." })
      else if (body.closed === false) toast.success(`Reopened ${c.name}`)
      else toast.success("Case updated")
      setEditing(false)
      setClosing(false)
    },
  })

  return (
    <section className="flex flex-col gap-12">
      <Button variant="quiet" className="self-start" onClick={onBack}>
        <ArrowLeft aria-hidden />
        All cases
      </Button>
      <QueryView query={current} skeleton={<RowsSkeleton rows={6} />}>
        {(c) => (
          <div className="flex flex-col gap-12">
            <header className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
              <div className="flex flex-col gap-3">
                <div className="flex flex-wrap items-center gap-3">
                  <h2 className="text-h2">{c.name}</h2>
                  <StatusBadge tone={c.closedAt ? "neutral" : "success"}>{c.closedAt ? "Closed" : "Open"}</StatusBadge>
                </div>
                {c.description && <p className="max-w-[72ch] text-fg-secondary">{c.description}</p>}
                <p className="text-sm text-fg-tertiary">
                  Created {formatDateTime(c.createdAt)}
                  {c.createdBy && ` by ${c.createdBy}`}
                  {c.closedAt && `, closed ${formatDateTime(c.closedAt)}`}
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-3">
                <Button
                  variant="secondary"
                  onClick={() => {
                    form.reset({ name: c.name, description: c.description })
                    setEditing(true)
                  }}
                >
                  <Pencil aria-hidden />
                  Edit details
                </Button>
                {c.closedAt ? (
                  <Button variant="secondary" loading={update.isPending} onClick={() => update.mutate({ closed: false })}>
                    Reopen case
                  </Button>
                ) : (
                  <Button variant="secondary" onClick={() => setClosing(true)}>
                    <Lock aria-hidden />
                    Close case
                  </Button>
                )}
              </div>
            </header>
            <CaseSearch current={c} directory={directory.data} onSaved={refresh} />
            <CaseHolds current={c} directory={directory.data} />
            <FormDialog
              open={editing}
              onOpenChange={setEditing}
              title="Edit case"
              submitLabel="Save case"
              pending={update.isPending}
              onSubmit={form.handleSubmit((v) => update.mutate(v))}
            >
              <CaseFields form={form} />
            </FormDialog>
            <ConfirmDialog
              open={closing}
              onOpenChange={setClosing}
              title={`Close ${c.name}?`}
              description={
                c.holds
                  ? `${plural(c.holds, "mailbox", "mailboxes")} on hold for this case ${c.holds === 1 ? "is" : "are"} released, unless another case holds ${c.holds === 1 ? "it" : "them"}. Retention policies and the clean-up of recoverable items apply again. You can still search and export.`
                  : "No mailboxes are on hold for this case. You can still search and export."
              }
              confirmLabel="Close case"
              pending={update.isPending}
              onConfirm={() => update.mutate({ closed: true })}
            />
          </div>
        )}
      </QueryView>
    </section>
  )
}

function CaseSearch({ current, directory, onSaved }: { current: ComplianceCase; directory?: ComplianceDirectory; onSaved: (c: ComplianceCase) => void }) {
  const form = useForm<CaseCriteria>({ resolver: zodResolver(criteriaSchema), defaultValues: current.criteria })
  const scope = useWatch({ control: form.control, name: "scope" })
  const dirty = form.formState.isDirty

  const run = useMutation({
    mutationFn: async (criteria: CaseCriteria) => {
      if (dirty) {
        const saved = await api<ComplianceCase>(`/compliance/cases/${current.id}`, { method: "PATCH", body: { criteria } })
        form.reset(saved.criteria)
        onSaved(saved)
      }
      return api<CaseSearchResult>(`/compliance/cases/${current.id}/search`, { method: "POST" })
    },
  })
  const result = run.data

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Search"
        description="Searches every folder of each mailbox in scope, including disabled mailboxes that are on hold. Running or exporting a search is recorded in the audit log."
        actions={
          <>
            {(["eml", "mbox"] as const).map((format) =>
              dirty ? (
                <Button key={format} variant="secondary" disabled>
                  <Download aria-hidden />
                  {format === "eml" ? "Export .eml" : "Export mbox"}
                </Button>
              ) : (
                <Button key={format} variant="secondary" asChild>
                  <a href={apiUrl(`/compliance/cases/${current.id}/export`, { format })} download>
                    <Download aria-hidden />
                    {format === "eml" ? "Export .eml" : "Export mbox"}
                  </a>
                </Button>
              )
            )}
          </>
        }
      />
      <form noValidate onSubmit={form.handleSubmit((v) => run.mutate(v))} className="flex flex-col gap-6">
        <Controller
          control={form.control}
          name="scope"
          render={({ field }) => (
            <div className="flex flex-col gap-2">
              <span className="text-sm font-medium">Where to search</span>
              <SegmentedControl label="Where to search" options={scopes} value={field.value} onChange={field.onChange} />
            </div>
          )}
        />
        {scope === "mailboxes" && (
          <Controller
            control={form.control}
            name="mailboxIds"
            render={({ field, fieldState }) => (
              <Field id="case-mailboxes" label="Mailboxes" error={fieldState.error?.message}>
                {directory ? (
                  <SelectionList
                    id="case-mailboxes"
                    label="Mailboxes"
                    items={directory.mailboxes.map((m) => ({ id: m.id, label: m.address, hint: m.retainedAt ? "Retained" : m.active ? undefined : "Inactive" }))}
                    value={field.value}
                    onChange={field.onChange}
                  />
                ) : (
                  <RowsSkeleton rows={2} />
                )}
              </Field>
            )}
          />
        )}
        {scope === "domains" && (
          <Controller
            control={form.control}
            name="domainIds"
            render={({ field, fieldState }) => (
              <Field id="case-domains" label="Domains" error={fieldState.error?.message}>
                {directory ? (
                  <SelectionList id="case-domains" label="Domains" items={directory.domains.map((d) => ({ id: d.id, label: d.name }))} value={field.value} onChange={field.onChange} />
                ) : (
                  <RowsSkeleton rows={2} />
                )}
              </Field>
            )}
          />
        )}
        <div className="grid gap-4 md:grid-cols-2">
          {textFields.map((f) => (
            <Field key={f.name} id={`case-${f.name}`} label={f.label} hint={f.hint} error={form.formState.errors[f.name]?.message}>
              <Input id={`case-${f.name}`} autoComplete="off" aria-invalid={!!form.formState.errors[f.name]} {...form.register(f.name)} />
            </Field>
          ))}
          <div className="grid grid-cols-2 gap-4">
            <Field id="case-since" label="Received from" error={form.formState.errors.since?.message}>
              <Input id="case-since" type="date" {...form.register("since")} />
            </Field>
            <Field id="case-until" label="Received until" error={form.formState.errors.until?.message}>
              <Input id="case-until" type="date" aria-invalid={!!form.formState.errors.until} {...form.register("until")} />
            </Field>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" loading={run.isPending}>
            <Search aria-hidden />
            {dirty ? "Save and search" : "Search"}
          </Button>
          {dirty && <p className="text-sm text-fg-tertiary">Exports use the saved search. Save it to export these terms.</p>}
        </div>
      </form>
      {run.isPending ? (
        <RowsSkeleton rows={4} />
      ) : (
        result && (
          <div className="animate-content flex flex-col gap-4">
            <p className="text-fg-secondary">
              {result.truncated ? `Showing the first ${result.results.length} matches` : plural(result.results.length, "match", "matches")} in{" "}
              {plural(result.searched, "mailbox", "mailboxes")}.
              {result.truncated && " Export the case to get every match."}
            </p>
            {result.skipped.length > 0 && (
              <p role="status" className="text-sm text-warning">
                Not searched because they are disabled and not on hold: {result.skipped.join(", ")}
              </p>
            )}
            {result.failed.length > 0 && (
              <p role="alert" className="text-sm text-danger">
                {result.failed.map((f) => `${f.mailbox}: ${f.error}`).join(" ")}
              </p>
            )}
            {result.results.length === 0 ? (
              <EmptyState icon={Search} title="No messages match" description="Widen the dates or remove a search term." />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Date</TableHead>
                    <TableHead>Mailbox</TableHead>
                    <TableHead>From</TableHead>
                    <TableHead>Subject</TableHead>
                    <TableHead>Size</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {result.results.map((h) => (
                    <TableRow key={`${h.mailboxId}/${h.folder}/${h.uid}`}>
                      <TableCell className="whitespace-nowrap">{formatDateTime(h.date)}</TableCell>
                      <TableCell>
                        <span className="font-mono text-sm">{h.mailbox}</span>
                        <span className="block text-sm text-fg-tertiary">{h.folder}</span>
                      </TableCell>
                      <TableCell>{h.from.map(formatAddress).join(", ") || "—"}</TableCell>
                      <TableCell>
                        {h.subject || "(no subject)"}
                        {h.attachments.length > 0 && <span className="block text-sm text-fg-tertiary">{h.attachments.join(", ")}</span>}
                      </TableCell>
                      <TableCell className="whitespace-nowrap">{formatBytes(h.size)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        )
      )}
    </section>
  )
}

function CaseHolds({ current, directory }: { current: ComplianceCase; directory?: ComplianceDirectory }) {
  const qc = useQueryClient()
  const holds = useQuery(holdsQuery(current.id))
  const [adding, setAdding] = useState(false)
  const [chosen, setChosen] = useState<string[]>([])
  const [scoped, setScoped] = useState(false)
  const terms = describeTerms(current.criteria)
  const [releasing, setReleasing] = useState<Hold | null>(null)
  const held = new Set((holds.data ?? []).map((h) => h.mailboxId))

  function refresh() {
    qc.invalidateQueries({ queryKey: ["compliance-holds", current.id] })
    qc.invalidateQueries({ queryKey: ["compliance-case", current.id] })
    qc.invalidateQueries({ queryKey: ["compliance-cases", current.orgId] })
    qc.invalidateQueries({ queryKey: ["compliance-directory", current.orgId] })
  }

  const add = useMutation({
    mutationFn: (mailboxIds: string[]) =>
      api<{ added: number }>(`/compliance/cases/${current.id}/holds`, { method: "POST", body: { mailboxIds, criteria: scoped && terms ? current.criteria : null } }),
    onSuccess: (r) => {
      toast.success(`Placed ${plural(r.added, "mailbox", "mailboxes")} on hold`)
      refresh()
      setAdding(false)
      setChosen([])
      setScoped(false)
    },
  })
  const release = useMutation({
    mutationFn: (h: Hold) => api(`/compliance/cases/${current.id}/holds/${h.mailboxId}`, { method: "DELETE" }),
    onSuccess: (_d, h) => {
      toast.success(`Released the hold on ${h.address}`)
      refresh()
      setReleasing(null)
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Holds"
        description="Held mail is never removed for good: not by retention policies, not by the clean-up of recoverable items, and not when the person deletes it, which only hides it in their recoverable items. Deleting a held mailbox keeps it as an inactive, retained mailbox. People are not told their mailbox is on hold."
        actions={
          <Button disabled={!!current.closedAt} onClick={() => setAdding(true)}>
            <Lock aria-hidden />
            Place on hold
          </Button>
        }
      />
      <QueryView
        query={holds}
        empty={<EmptyState icon={Lock} title="No holds" description={current.closedAt ? "Closing the case released its holds." : "Place mailboxes on hold to keep their mail for this case."} />}
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Mailbox</TableHead>
                <TableHead>Keeps</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Placed</TableHead>
                <TableHead className="w-16">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((h) => (
                <TableRow key={h.mailboxId}>
                  <TableCell>
                    <span className="font-mono text-sm">{h.address}</span>
                    {h.displayName && <span className="block text-sm text-fg-tertiary">{h.displayName}</span>}
                  </TableCell>
                  <TableCell className="max-w-[40ch]">{h.criteria ? `Messages matching ${describeTerms(h.criteria)}` : "Every message"}</TableCell>
                  <TableCell>{mailboxState(h)}</TableCell>
                  <TableCell>
                    {formatDate(h.createdAt)}
                    {h.createdBy && <span className="text-fg-tertiary"> by {h.createdBy}</span>}
                  </TableCell>
                  <TableCell>
                    <IconButton label={`Release the hold on ${h.address}`} size="icon-sm" onClick={() => setReleasing(h)}>
                      <ShieldOff />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>
      <FormDialog
        open={adding}
        onOpenChange={(open) => {
          setAdding(open)
          if (!open) {
            setChosen([])
            setScoped(false)
          }
        }}
        title="Place mailboxes on hold"
        description="The hold applies at once. Open mail app sessions of these mailboxes are ended so the hold also reaches them; mail apps reconnect on their own. Placing a hold again on a held mailbox replaces what it keeps."
        submitLabel="Place on hold"
        pending={add.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (chosen.length) add.mutate(chosen)
        }}
      >
        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium">What to keep</span>
          <SegmentedControl
            label="What to keep"
            options={[
              { value: "all", label: "Every message" },
              { value: "scoped", label: "Messages matching the search" },
            ]}
            value={scoped ? "scoped" : "all"}
            onChange={(v) => setScoped(v === "scoped" && !!terms)}
          />
          <p className="text-sm text-fg-tertiary">
            {!terms
              ? "Save search terms or dates for this case to keep only the messages that match them."
              : scoped
                ? `Keeps messages matching ${describeTerms(current.criteria)}. Other mail follows the usual retention.`
                : "Keeps every message in the mailbox, including mail deleted later."}
          </p>
        </div>
        {directory ? (
          <SelectionList
            id="hold-mailboxes"
            label="Mailboxes"
            items={directory.mailboxes.filter((m) => !held.has(m.id)).map((m) => ({ id: m.id, label: m.address, hint: m.held ? "On hold for another case" : undefined }))}
            value={chosen}
            onChange={setChosen}
          />
        ) : (
          <RowsSkeleton rows={3} />
        )}
      </FormDialog>
      <ConfirmDialog
        open={!!releasing}
        onOpenChange={(open) => !open && setReleasing(null)}
        title={`Release the hold on ${releasing?.address}?`}
        description={
          releasing?.retainedAt
            ? "This mailbox was deleted while on hold. Once no case holds it, an administrator can delete it for good."
            : "Unless another case holds it, retention policies and the clean-up of recoverable items apply to all of its mail again, and it can be deleted."
        }
        confirmLabel="Release hold"
        pending={release.isPending}
        onConfirm={() => releasing && release.mutate(releasing)}
      />
    </section>
  )
}

"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Download } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { MailboxPicker } from "@/components/settings/mail-rules"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { SwitchField } from "@/components/switch-field"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api, errorMessage } from "@/lib/api"
import { formatDateTime, plural } from "@/lib/format"
import { mailboxesQuery, myMailboxesQuery } from "@/lib/queries"
import type { Mailbox, MailImport, MailImportSchedule } from "@/lib/types"

const providers = [
  { label: "Gmail", host: "imap.gmail.com" },
  { label: "iCloud Mail", host: "imap.mail.me.com" },
  { label: "Yahoo Mail", host: "imap.mail.yahoo.com" },
  { label: "Fastmail", host: "imap.fastmail.com" },
]

const schema = z.object({
  host: z.string().trim().min(1, "Enter the IMAP server, such as imap.gmail.com."),
  port: z.coerce.number<number>().int().min(1).max(65535),
  security: z.enum(["tls", "starttls"]),
  username: z.string().trim(),
  password: z.string(),
  batch: z.string(),
  intervalMinutes: z.coerce.number<number>().int().min(0).max(10080),
  endsAt: z.string(),
  syncFlags: z.boolean(),
  syncDeletions: z.boolean(),
})

type Values = z.infer<typeof schema>

const statuses: Record<MailImport["status"], { tone: "success" | "warning" | "danger" | "info" | "neutral"; label: string }> = {
  running: { tone: "info", label: "Copying" },
  done: { tone: "success", label: "Done" },
  failed: { tone: "danger", label: "Failed" },
  cancelled: { tone: "neutral", label: "Cancelled" },
  interrupted: { tone: "warning", label: "Interrupted" },
}

export function ImportMail({ orgId }: { orgId?: string } = {}) {
  const qc = useQueryClient()
  const personalMailboxes = useQuery({ ...myMailboxesQuery, enabled: !orgId, select: (list) => list.filter((m) => m.access !== "read" && m.protocols.includes("webmail")) })
  const orgMailboxes = useQuery({
    ...mailboxesQuery(orgId ?? ""),
    enabled: !!orgId,
  })
  const [chosen, setChosen] = useState<string | null>(null)
  const mailboxId = chosen ?? personalMailboxes.data?.[0]?.id ?? null
  const schedulePath = orgId ? `/organizations/${orgId}/import-schedules` : `/mailboxes/${mailboxId}/import-schedules`
  const [open, setOpen] = useState(false)
  const [history, setHistory] = useState<string | null>(null)
  const [selected, setSelected] = useState<string[]>([])
  const schedules = useQuery({
    queryKey: ["import-schedules", schedulePath],
    queryFn: () => api<MailImportSchedule[]>(schedulePath),
    enabled: !!(orgId || mailboxId),
    refetchInterval: (q) => q.state.data?.some((j) => j.status === "scheduled" || j.latestRun?.status === "running") ? 2000 : false,
  })
  const importPath = orgId ? `${schedulePath}/${history}/runs` : `/mailboxes/${mailboxId}/imports`
  const imports = useQuery({
    queryKey: ["imports", importPath],
    queryFn: () => api<MailImport[]>(importPath),
    enabled: !!(orgId ? history : mailboxId),
    refetchInterval: (q) => (schedules.data?.some((j) => j.status === "scheduled") || q.state.data?.some((i) => i.status === "running")) ? 2000 : false,
  })
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { host: "", port: 993, security: "tls", username: "", password: "", batch: "", intervalMinutes: orgId ? 15 : 0, endsAt: "", syncFlags: false, syncDeletions: false },
  })
  const interval = useWatch({ control: form.control, name: "intervalMinutes" })
  const start = useMutation({
    gcTime: 0,
    mutationFn: async ({ host, port, security, username, password, batch, intervalMinutes, endsAt, syncFlags, syncDeletions }: Values) => {
      const connection = { host, port, security }
      const schedule = { ...connection, intervalMinutes, endsAt: endsAt ? new Date(endsAt).toISOString() : null, syncFlags, syncDeletions }
      if (orgId) {
        const rows = batch.split(/\r?\n/).filter((line) => line.trim())
        if (rows.length < 1 || rows.length > 100 || intervalMinutes < 1) throw new Error("Enter 1 to 100 mailbox rows and a repeat interval.")
        const imports = rows.map((line, index) => {
          const [address, sourceUser, sourcePassword, extra] = line.split("\t")
          const mailbox = orgMailboxes.data?.find((m) => m.active && m.address.toLowerCase() === address.trim().toLowerCase())
          if (!mailbox || !sourceUser?.trim() || !sourcePassword || extra !== undefined) {
            throw new Error(`Row ${index + 1} needs an active destination address, a source username and a password, separated by tabs.`)
          }
          return { ...schedule, mailboxId: mailbox.id, username: sourceUser, password: sourcePassword }
        })
        await api(schedulePath, { method: "POST", body: { imports } })
      } else {
        if (!username || !password) throw new Error("Enter the source username and password.")
        if (intervalMinutes) {
          await api(schedulePath, { method: "POST", body: { ...schedule, username, password } })
        } else {
          await api(importPath, { method: "POST", body: { ...connection, username, password } })
        }
      }
    },
    onSuccess: (_, values) => {
      toast.success(values.intervalMinutes ? "Import scheduled" : "Import started")
      setOpen(false)
      form.reset()
      qc.invalidateQueries({ queryKey: ["import-schedules", schedulePath] })
      qc.invalidateQueries({ queryKey: ["imports", importPath] })
    },
  })
  const cancel = useMutation({
    mutationFn: (id: string) => api(`${importPath}/${id}/cancel`, { method: "POST" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["imports", importPath] }),
    onError: (err) => toast.error(errorMessage(err)),
  })
  const stop = useMutation({
    mutationFn: (ids: string[]) => orgId
      ? api(`${schedulePath}/stop`, { method: "POST", body: { ids } })
      : api(`${schedulePath}/${ids[0]}`, { method: "DELETE" }),
    onSuccess: () => {
      setSelected([])
      qc.invalidateQueries({ queryKey: ["import-schedules", schedulePath] })
    },
    onError: (err) => toast.error(errorMessage(err)),
  })

  const running = imports.data?.some((i) => i.status === "running")
  const activeSelection = selected.filter((id) => schedules.data?.some((j) => j.id === id && j.status === "scheduled"))
  const canStart = orgId ? orgMailboxes.data?.some((m) => m.active) : mailboxId && !running

  return (
    <section id="import" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader
        title={orgId ? "Mail imports" : "Import mail"}
        description={orgId ? "Schedule recurring copies from an old provider and track their progress through cutover." : "Copy mail from another provider once or keep importing new messages until cutover."}
        actions={
          <>
            {!orgId && <MailboxPicker mailboxes={personalMailboxes.data} value={mailboxId} onChange={setChosen} />}
            <Button variant="secondary" disabled={!canStart} onClick={() => setOpen(true)}>
              <Download aria-hidden />
              {orgId ? "Schedule imports" : "Import from another provider"}
            </Button>
          </>
        }
      />
      {orgMailboxes.isError && <p role="alert" className="text-sm text-danger">{errorMessage(orgMailboxes.error)}</p>}
      {orgId && activeSelection.length > 0 && (
        <div><Button variant="secondary" disabled={stop.isPending} onClick={() => stop.mutate(activeSelection)}>Stop {plural(activeSelection.length, "schedule")}</Button></div>
      )}
      {(orgId || mailboxId) && (orgId || schedules.data?.length || schedules.isError ? (
        <QueryView query={schedules} empty={<EmptyState icon={Download} title="No scheduled imports" description="Schedule your first batch of mailbox imports." />}>
          {(jobs) => (
            <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
              {jobs.map((job) => (
                <li key={job.id} className="flex flex-wrap items-center justify-between gap-4 p-4">
                  <div className="flex items-start gap-4">
                    {orgId && job.status === "scheduled" && (
                      <input
                        type="checkbox"
                        className="mt-1 size-4"
                        aria-label={`Select schedule for ${job.address}`}
                        checked={selected.includes(job.id)}
                        disabled={selected.length >= 100 && !selected.includes(job.id)}
                        onChange={(event) => setSelected(event.target.checked ? [...selected, job.id] : selected.filter((id) => id !== job.id))}
                      />
                    )}
                    <div className="flex flex-col gap-1">
                      <span className="font-medium">{orgId ? job.address : job.username}</span>
                      <span className="text-sm text-fg-secondary">{job.host} · Every {plural(job.intervalMinutes, "minute")}</span>
                      {job.endsAt && <span className="text-sm text-fg-tertiary">Ends {formatDateTime(job.endsAt)}</span>}
                      {job.nextRunAt && job.latestRun?.status !== "running" && <span className="text-sm text-fg-tertiary">Next run {formatDateTime(job.nextRunAt)}</span>}
                      {job.latestRun?.error && <span className="text-sm text-danger">{job.latestRun.error}</span>}
                    </div>
                  </div>
                  <div className="flex flex-wrap items-center gap-3">
                    <StatusBadge tone={job.status === "scheduled" ? "info" : "neutral"}>{job.status === "scheduled" ? "Scheduled" : job.status === "stopped" ? "Stopped" : "Expired"}</StatusBadge>
                    {job.latestRun && <StatusBadge tone={statuses[job.latestRun.status].tone}>{statuses[job.latestRun.status].label} · {job.latestRun.progress.copied ?? 0} copied</StatusBadge>}
                    {orgId && <Button variant="secondary" size="sm" aria-expanded={history === job.id} aria-controls="import-run-history" onClick={() => setHistory(history === job.id ? null : job.id)}>{history === job.id ? "Hide runs" : "View runs"}</Button>}
                    {!orgId && job.status === "scheduled" && <Button variant="secondary" size="sm" disabled={stop.isPending} onClick={() => stop.mutate([job.id])}>Stop</Button>}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </QueryView>
      ) : null)}
      <div id="import-run-history">
        {(orgId ? history : mailboxId) && (
        <QueryView query={imports} empty={<EmptyState icon={Download} title="No imports yet" description="Bring over the mail from your old address." />}>
          {(items) => (
            <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
              {items.map((i) => {
                const status = statuses[i.status]
                const p = i.progress
                return (
                  <li key={i.id} className="flex flex-col gap-2 p-4">
                    <div className="flex flex-wrap items-center justify-between gap-3">
                      <div className="flex flex-col gap-1">
                        <span className="font-medium">
                          {i.username} <span className="text-fg-tertiary">on {i.host}</span>
                        </span>
                        <span className="text-sm text-fg-tertiary">Started {formatDateTime(i.createdAt)}</span>
                      </div>
                      <div className="flex items-center gap-3">
                        <StatusBadge tone={status.tone}>{status.label}</StatusBadge>
                        {!orgId && !i.scheduleId && i.status === "running" && (
                          <Button variant="secondary" size="sm" disabled={cancel.isPending} onClick={() => cancel.mutate(i.id)}>
                            Cancel
                          </Button>
                        )}
                      </div>
                    </div>
                    <p className="text-sm text-fg-secondary">
                      {p.foldersTotal ? `${p.foldersDone} of ${plural(p.foldersTotal, "folder")}` : "Connecting"} · {plural(p.copied ?? 0, "message")} copied
                      {p.skipped ? ` · ${p.skipped} skipped` : ""}
                      {p.flagsUpdated ? ` · ${plural(p.flagsUpdated, "flag update")}` : ""}
                      {p.deleted ? ` · ${p.deleted} deleted` : ""}
                      {i.status === "running" && p.folder ? ` · now copying ${p.folder}` : ""}
                    </p>
                    {i.error && <p className="text-sm text-danger">{i.error}</p>}
                    {i.status === "interrupted" && <p className="text-sm text-fg-tertiary">The server stopped during this run. {i.scheduleId ? "Active schedules retry automatically." : "Start again to copy the remaining messages."}</p>}
                  </li>
                )
              })}
            </ul>
          )}
        </QueryView>
        )}
      </div>

      <FormDialog
        open={open}
        onOpenChange={(o) => {
          setOpen(o)
          if (!o) {
            start.reset()
            form.reset()
          }
        }}
        title={orgId ? "Schedule imports" : "Import from another provider"}
        description="Aether reads the source account over IMAP. Recurring imports store source passwords encrypted until the schedule stops or expires."
        submitLabel={orgId || interval > 0 ? "Schedule imports" : "Start import"}
        pending={start.isPending}
        onSubmit={form.handleSubmit((v) => start.mutate(v))}
      >
        <div className="flex flex-wrap gap-2">
          {providers.map((p) => (
            <Button
              key={p.host}
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => {
                form.setValue("host", p.host)
                form.setValue("port", 993)
                form.setValue("security", "tls")
              }}
            >
              {p.label}
            </Button>
          ))}
        </div>
        <Field id="import-host" label="IMAP server" error={form.formState.errors.host?.message}>
          <Input id="import-host" autoComplete="off" placeholder="imap.example.com" {...form.register("host")} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="import-port" label="Port" error={form.formState.errors.port?.message}>
            <Input id="import-port" inputMode="numeric" {...form.register("port")} />
          </Field>
          <Controller
            control={form.control}
            name="security"
            render={({ field }) => (
              <Field id="import-security" label="Security">
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger id="import-security" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="tls">SSL/TLS</SelectItem>
                    <SelectItem value="starttls">STARTTLS</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            )}
          />
        </div>
        {orgId ? (
          <Field id="import-batch" label="Mailbox rows" hint="Paste one row per mailbox with three tab-separated columns in this order: destination email, source username, source password. Passwords are visible while editing.">
            <Textarea id="import-batch" required autoComplete="off" spellCheck={false} {...form.register("batch")} />
          </Field>
        ) : (
          <>
            <Field id="import-username" label="Username">
              <Input id="import-username" required autoComplete="username" autoCapitalize="none" spellCheck={false} {...form.register("username")} />
            </Field>
            <Field id="import-password" label="Password" hint="Use an app password if your provider requires one.">
              <Input id="import-password" required type="password" autoComplete="current-password" {...form.register("password")} />
            </Field>
          </>
        )}
        <Field id="import-interval" label="Repeat interval (minutes)" hint={orgId ? "Enter 1 to 10080 minutes." : "Use 0 for a one-time copy."} error={form.formState.errors.intervalMinutes?.message}>
          <Input id="import-interval" type="number" min={orgId ? 1 : 0} max={10080} required {...form.register("intervalMinutes")} />
        </Field>
        {(orgId || interval > 0) && (
          <>
            <Field id="import-end" label="End time (optional)" hint="Uses this device’s time zone.">
              <Input id="import-end" type="datetime-local" {...form.register("endsAt")} />
            </Field>
            <SwitchField id="import-flags" control={form.control} name="syncFlags" label="Sync read, starred, answered and draft flags" hint="Source flags are applied to messages this schedule copied." />
            <SwitchField id="import-deletions" control={form.control} name="syncDeletions" label="Sync source deletions" hint="Permanently removes copies made by this schedule when their source messages are deleted from the same folder." />
          </>
        )}
        {start.error && <p role="alert" className="text-sm text-danger">{errorMessage(start.error)}</p>}
      </FormDialog>
    </section>
  )
}

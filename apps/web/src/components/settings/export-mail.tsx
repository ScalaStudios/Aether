"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Download } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { MailboxPicker } from "@/components/settings/mail-rules"
import { SwitchField } from "@/components/switch-field"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api, apiUrl } from "@/lib/api"
import { formatBytes, formatDateTime } from "@/lib/format"
import { myMailboxesQuery } from "@/lib/queries"
import type { Mailbox, MailboxExport } from "@/lib/types"

const schema = z
  .object({
    format: z.enum(["zip", "mbox"]),
    folder: z.string().trim(),
    since: z.string(),
    until: z.string(),
    includeCalendar: z.boolean(),
    includeContacts: z.boolean(),
  })
  .refine((v) => !v.since || !v.until || v.until >= v.since, { message: "The end is before the start.", path: ["until"] })

type Values = z.infer<typeof schema>

const defaults: Values = { format: "zip", folder: "", since: "", until: "", includeCalendar: true, includeContacts: true }

const statuses: Record<MailboxExport["status"], { tone: "success" | "warning" | "danger" | "info" | "neutral"; label: string }> = {
  pending: { tone: "info", label: "Queued" },
  running: { tone: "info", label: "Preparing" },
  ready: { tone: "success", label: "Ready" },
  failed: { tone: "danger", label: "Failed" },
  expired: { tone: "neutral", label: "Expired" },
}

function downloadExport(mailboxId: string, id: string) {
  const a = document.createElement("a")
  a.href = apiUrl(`/mailboxes/${mailboxId}/exports/${id}/download`)
  a.click()
}

export function ExportForm({ mailboxId, onStarted }: { mailboxId: string; onStarted?: () => void }) {
  const qc = useQueryClient()
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: defaults })
  const exports = useQuery({
    queryKey: ["exports", mailboxId],
    queryFn: () => api<MailboxExport[]>(`/mailboxes/${mailboxId}/exports`),
    refetchInterval: (q) => (q.state.data?.some((e) => e.status === "pending" || e.status === "running") ? 2000 : false),
  })
  const start = useMutation({
    mutationFn: (values: Values) =>
      api<MailboxExport>(`/mailboxes/${mailboxId}/exports`, {
        method: "POST",
        body: {
          format: values.format,
          folder: values.folder || undefined,
          since: values.since || undefined,
          until: values.until || undefined,
          includeCalendar: values.includeCalendar,
          includeContacts: values.includeContacts,
        },
      }),
    onSuccess: () => {
      toast.success("Export started", { description: "Aether prepares it in the background. The download link lasts 7 days once it is ready." })
      form.reset(defaults)
      onStarted?.()
      qc.invalidateQueries({ queryKey: ["exports", mailboxId] })
    },
  })

  return (
    <div className="flex flex-col gap-6">
      <form noValidate className="flex flex-col gap-4" onSubmit={form.handleSubmit((v) => start.mutate(v))}>
        <Field id="export-format" label="Format">
          <Select value={form.watch("format")} onValueChange={(format) => form.setValue("format", format as Values["format"])}>
            <SelectTrigger id="export-format" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="zip">ZIP of .eml files</SelectItem>
              <SelectItem value="mbox">mbox</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field id="export-folder" label="Folder" hint="Leave empty to export every folder." error={form.formState.errors.folder?.message}>
          <Input id="export-folder" autoComplete="off" spellCheck={false} placeholder="INBOX" {...form.register("folder")} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="export-since" label="From" hint="Optional.">
            <Input id="export-since" type="date" {...form.register("since")} />
          </Field>
          <Field id="export-until" label="Until" hint="Optional. Includes this day." error={form.formState.errors.until?.message}>
            <Input id="export-until" type="date" aria-invalid={!!form.formState.errors.until} {...form.register("until")} />
          </Field>
        </div>
        <SwitchField control={form.control} name="includeCalendar" id="export-calendar" label="Include calendars" hint="iCalendar files for the mailbox owner's calendars." />
        <SwitchField control={form.control} name="includeContacts" id="export-contacts" label="Include contacts" hint="vCard files for the mailbox owner's address books." />
        <Button type="submit" className="self-start" loading={start.isPending}>
          <Download aria-hidden />
          Start export
        </Button>
      </form>
      <QueryView
        query={exports}
        skeleton={null}
        empty={<p className="text-sm text-fg-tertiary">No exports yet.</p>}
      >
        {(items) => (
          <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
            {items.map((item) => {
              const status = statuses[item.status]
              return (
                <li key={item.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                  <div className="flex min-w-0 flex-1 flex-col">
                    <span>{item.format === "mbox" ? "mbox" : "ZIP of .eml files"}{item.folder ? ` · ${item.folder}` : ""}</span>
                    <span className="text-sm text-fg-tertiary">
                      {formatDateTime(item.createdAt)}
                      {item.status === "ready" && item.bytes ? ` · ${formatBytes(item.bytes)}` : ""}
                      {item.expiresAt && item.status === "ready" ? ` · link expires ${formatDateTime(item.expiresAt)}` : ""}
                      {item.error ? ` · ${item.error}` : ""}
                    </span>
                  </div>
                  <StatusBadge tone={status.tone}>{status.label}</StatusBadge>
                  {item.status === "ready" && (
                    <Button type="button" variant="secondary" size="sm" onClick={() => downloadExport(mailboxId, item.id)}>
                      Download
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </QueryView>
    </div>
  )
}

export function ExportMail() {
  const mailboxes = useQuery(myMailboxesQuery)
  const [chosen, setChosen] = useState<string | null>(null)
  const mailboxId = chosen ?? mailboxes.data?.[0]?.id ?? null

  return (
    <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <SectionHeader
        title="Export mailbox"
        description="Download mail as a ZIP of .eml files or as mbox, with calendars and contacts. Large exports run in the background. The download link expires 7 days after the file is ready."
        actions={mailboxId ? <MailboxPicker mailboxes={mailboxes.data} value={mailboxId} onChange={setChosen} /> : undefined}
      />
      {mailboxes.data?.length === 0 ? (
        <EmptyState icon={Download} title="No mailbox" description="You need a mailbox before you can export mail." />
      ) : (
        mailboxId && <ExportForm key={mailboxId} mailboxId={mailboxId} />
      )}
    </section>
  )
}

export function ExportMailboxDialog({ mailbox, onOpenChange }: { mailbox: Mailbox | null; onOpenChange: (open: boolean) => void }) {
  return (
    <Dialog open={!!mailbox} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[800px]">
        <DialogHeader>
          <DialogTitle>Export {mailbox?.address}</DialogTitle>
          <DialogDescription>The file is prepared in the background. This export is recorded in the audit log. The download link expires 7 days after the file is ready.</DialogDescription>
        </DialogHeader>
        {mailbox && <ExportForm key={mailbox.id} mailboxId={mailbox.id} />}
      </DialogContent>
    </Dialog>
  )
}

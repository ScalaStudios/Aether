"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Download } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { MailboxPicker } from "@/components/settings/mail-rules"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api, errorMessage } from "@/lib/api"
import { formatDateTime, plural } from "@/lib/format"
import { myMailboxesQuery } from "@/lib/queries"
import type { MailImport } from "@/lib/types"

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
  username: z.string().trim().min(1, "Enter the username, usually the full address."),
  password: z.string().min(1, "Enter the password or app password."),
})

type Values = z.infer<typeof schema>

const statuses: Record<MailImport["status"], { tone: "success" | "warning" | "danger" | "info" | "neutral"; label: string }> = {
  running: { tone: "info", label: "Copying" },
  done: { tone: "success", label: "Done" },
  failed: { tone: "danger", label: "Failed" },
  cancelled: { tone: "neutral", label: "Cancelled" },
  interrupted: { tone: "warning", label: "Interrupted" },
}

export function ImportMail() {
  const qc = useQueryClient()
  const mailboxes = useQuery({ ...myMailboxesQuery, select: (list) => list.filter((m) => m.access !== "read") })
  const [chosen, setChosen] = useState<string | null>(null)
  const mailboxId = chosen ?? mailboxes.data?.[0]?.id ?? null
  const [open, setOpen] = useState(false)
  const imports = useQuery({
    queryKey: ["imports", mailboxId],
    queryFn: () => api<MailImport[]>(`/mailboxes/${mailboxId}/imports`),
    enabled: !!mailboxId,
    refetchInterval: (q) => (q.state.data?.some((i) => i.status === "running") ? 2000 : false),
  })
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { host: "", port: 993, security: "tls", username: "", password: "" } })

  const start = useMutation({
    mutationFn: (values: Values) => api<MailImport>(`/mailboxes/${mailboxId}/imports`, { method: "POST", body: values }),
    onSuccess: () => {
      toast.success("Import started", { description: "You can leave this page. Aether keeps copying in the background." })
      setOpen(false)
      form.reset()
      qc.invalidateQueries({ queryKey: ["imports", mailboxId] })
    },
  })
  const cancel = useMutation({
    mutationFn: (id: string) => api(`/mailboxes/${mailboxId}/imports/${id}/cancel`, { method: "POST" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["imports", mailboxId] }),
  })

  const running = imports.data?.some((i) => i.status === "running")

  return (
    <section id="import" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader
        title="Import mail"
        description="Copy every folder from another provider into this mailbox over IMAP. Messages that are already here are skipped, so you can run an import again."
        actions={
          <>
            <MailboxPicker mailboxes={mailboxes.data} value={mailboxId} onChange={setChosen} />
            {mailboxId && (
              <Button variant="secondary" disabled={running} onClick={() => setOpen(true)}>
                <Download aria-hidden />
                Import from another provider
              </Button>
            )}
          </>
        }
      />
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
                      {i.status === "running" && (
                        <Button variant="secondary" size="sm" disabled={cancel.isPending} onClick={() => cancel.mutate(i.id)}>
                          Cancel
                        </Button>
                      )}
                    </div>
                  </div>
                  <p className="text-sm text-fg-secondary">
                    {p.foldersTotal ? `${p.foldersDone} of ${plural(p.foldersTotal, "folder")}` : "Connecting"} · {plural(p.copied ?? 0, "message")} copied
                    {p.skipped ? ` · ${p.skipped} already here` : ""}
                    {i.status === "running" && p.folder ? ` · now copying ${p.folder}` : ""}
                  </p>
                  {i.error && <p className="text-sm text-danger">{i.error}</p>}
                  {i.status === "interrupted" && <p className="text-sm text-fg-tertiary">Aether restarted during the import. Start it again; messages already copied are skipped.</p>}
                </li>
              )
            })}
          </ul>
        )}
      </QueryView>

      <FormDialog
        open={open}
        onOpenChange={(o) => {
          setOpen(o)
          if (!o) start.reset()
        }}
        title="Import from another provider"
        description="Aether signs in to the other account over IMAP and copies its folders. It only reads from the other account and never changes or deletes anything there."
        submitLabel="Start import"
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
        <Field id="import-username" label="Username" error={form.formState.errors.username?.message}>
          <Input id="import-username" autoComplete="off" placeholder="you@example.com" {...form.register("username")} />
        </Field>
        <Field
          id="import-password"
          label="Password"
          error={form.formState.errors.password?.message ?? (start.error ? errorMessage(start.error) : undefined)}
        >
          <Input id="import-password" type="password" autoComplete="off" {...form.register("password")} />
        </Field>
        <p className="text-sm text-fg-tertiary">
          Gmail, iCloud and Yahoo need an app password from their account settings, not your normal password. Aether uses it only for this import and does not store it.
        </p>
      </FormDialog>
    </section>
  )
}

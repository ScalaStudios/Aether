"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus, ShieldBan, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, ApiError, errorMessage } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { domainsQuery } from "@/lib/queries"
import type { SenderEntry } from "@/lib/types"

const schema = z.object({
  value: z.string().trim().min(1, "Enter an email address, domain or IP address."),
  action: z.enum(["block", "allow"]),
  domainId: z.string(),
  note: z.string().max(500, "Use up to 500 characters."),
  expires: z.string(),
})

type Values = z.infer<typeof schema>

const defaults: Values = { value: "", action: "block", domainId: "all", note: "", expires: "" }

export function SendersTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const key = ["sender-lists", orgId]
  const entries = useQuery({ queryKey: key, queryFn: () => api<SenderEntry[]>(`/organizations/${orgId}/sender-lists`) })
  const domains = useQuery(domainsQuery(orgId))
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<SenderEntry | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: defaults })
  const today = new Date().toLocaleDateString("en-CA")
  const domainName = (id: string | null) => (id ? `@${domains.data?.find((d) => d.id === id)?.name ?? "…"}` : "All domains")

  const add = useMutation({
    mutationFn: (v: Values) =>
      api<SenderEntry>(`/organizations/${orgId}/sender-lists`, {
        method: "POST",
        body: {
          value: v.value,
          action: v.action,
          domainId: v.domainId === "all" ? null : v.domainId,
          note: v.note,
          expiresAt: v.expires ? new Date(`${v.expires}T23:59:59`).toISOString() : null,
        },
      }),
    meta: { silent: true },
    onSuccess: (e) => {
      toast.success(e.action === "block" ? `Blocked ${e.value}` : `Allowed ${e.value}`)
      qc.invalidateQueries({ queryKey: key })
      setAdding(false)
      form.reset(defaults)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "invalid_sender") form.setError("value", { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const remove = useMutation({
    mutationFn: (e: SenderEntry) => api(`/organizations/${orgId}/sender-lists/${e.id}`, { method: "DELETE" }),
    onSuccess: (_d, e) => {
      toast.success(`Removed ${e.value}`)
      qc.invalidateQueries({ queryKey: key })
      setRemoving(null)
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Allowed and blocked senders"
        description="Mail from a blocked sender, domain or IP address is rejected while it is being delivered. Mail from an allowed address or domain skips spam filtering when it passes DMARC or SPF for that sender, but is still scanned for viruses and still rejected when its domain's DMARC policy says so. A block wins over an allow."
        actions={
          <Button onClick={() => setAdding(true)}>
            <Plus aria-hidden />
            Add sender
          </Button>
        }
      />
      <QueryView
        query={entries}
        empty={<EmptyState icon={ShieldBan} title="No allowed or blocked senders" description="Block an address or domain after a phishing campaign, or allow a partner whose mail keeps landing in Junk." />}
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Sender</TableHead>
                <TableHead>List</TableHead>
                <TableHead>Applies to</TableHead>
                <TableHead>Note</TableHead>
                <TableHead>Expires</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((e) => {
                const expired = !!e.expiresAt && Date.parse(e.expiresAt) <= Date.now()
                return (
                  <TableRow key={e.id}>
                    <TableCell className="font-mono text-sm break-all">{e.kind === "domain" ? `@${e.value}` : e.value}</TableCell>
                    <TableCell>
                      <StatusBadge tone={e.action === "block" ? "danger" : "success"}>{e.action === "block" ? "Blocked" : "Allowed"}</StatusBadge>
                    </TableCell>
                    <TableCell>{domainName(e.domainId)}</TableCell>
                    <TableCell className="max-w-64 truncate text-fg-secondary" title={e.note}>
                      {e.note || "—"}
                    </TableCell>
                    <TableCell>{expired ? <StatusBadge tone="neutral">Expired</StatusBadge> : e.expiresAt ? formatDate(e.expiresAt) : "Never"}</TableCell>
                    <TableCell>
                      <IconButton label={`Remove ${e.value}`} size="icon-sm" onClick={() => setRemoving(e)}>
                        <Trash2 />
                      </IconButton>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={adding}
        onOpenChange={(open) => {
          setAdding(open)
          if (!open) form.reset(defaults)
        }}
        title="Add sender"
        description="Adding a sender that is already listed for the same domains replaces its entry."
        submitLabel="Add sender"
        pending={add.isPending}
        onSubmit={form.handleSubmit((v) => add.mutate(v))}
      >
        <Field id="sender-value" label="Sender" hint="An email address, a domain such as example.com (which includes its subdomains), or an IP address or range." error={form.formState.errors.value?.message}>
          <Input id="sender-value" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!form.formState.errors.value} {...form.register("value")} />
        </Field>
        <Controller
          control={form.control}
          name="action"
          render={({ field }) => (
            <div className="flex flex-col gap-2">
              <span className="text-label text-fg-secondary">List</span>
              <SegmentedControl
                label="List"
                options={[
                  { value: "block", label: "Block" },
                  { value: "allow", label: "Allow" },
                ]}
                value={field.value}
                onChange={field.onChange}
              />
            </div>
          )}
        />
        <Controller
          control={form.control}
          name="domainId"
          render={({ field }) => (
            <Field id="sender-domain" label="Applies to">
              <Select value={field.value} onValueChange={field.onChange}>
                <SelectTrigger id="sender-domain" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All domains</SelectItem>
                  {domains.data?.map((d) => (
                    <SelectItem key={d.id} value={d.id}>
                      @{d.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
        />
        <Field id="sender-note" label="Note" hint="Optional. Why the sender is listed." error={form.formState.errors.note?.message}>
          <Textarea id="sender-note" rows={2} maxLength={500} {...form.register("note")} />
        </Field>
        <Field id="sender-expires" label="Expires" hint="Optional. The entry stops applying at the end of this day.">
          <Input id="sender-expires" type="date" min={today} {...form.register("expires")} />
        </Field>
      </FormDialog>
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(o) => !o && setRemoving(null)}
        title={`Remove ${removing?.value}?`}
        description={removing?.action === "block" ? "Mail from this sender is accepted again and filtered as usual." : "Mail from this sender is filtered for spam again."}
        confirmLabel="Remove sender"
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing)}
      />
    </section>
  )
}

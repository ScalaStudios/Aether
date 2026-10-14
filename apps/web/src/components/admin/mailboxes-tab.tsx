"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Inbox, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { DomainSelect } from "@/components/admin/domain-select"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatBytes } from "@/lib/format"
import type { Mailbox } from "@/lib/types"

const MB = 1024 * 1024

const schema = z.object({
  domainId: z.string().min(1, "Choose a domain."),
  localPart: z
    .string()
    .trim()
    .toLowerCase()
    .regex(/^[a-z0-9]([a-z0-9._+-]*[a-z0-9])?$/, "Use letters, digits, dots, hyphens, plus signs or underscores."),
  displayName: z.string().trim(),
  password: z.union([z.literal(""), z.string().min(12, "Use at least 12 characters, or leave it empty.")]),
  quotaMb: z.string().regex(/^\d*$/, "Enter a whole number of megabytes."),
})

type Values = z.infer<typeof schema>

export function MailboxesTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const mailboxes = useQuery({ queryKey: ["mailboxes", orgId], queryFn: () => api<Mailbox[]>(`/organizations/${orgId}/mailboxes`) })
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Mailbox | null>(null)
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { domainId: "", localPart: "", displayName: "", password: "", quotaMb: "" },
  })
  const errors = form.formState.errors

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["mailboxes", orgId] })
    qc.invalidateQueries({ queryKey: ["my-mailboxes"] })
  }

  const create = useMutation({
    mutationFn: ({ quotaMb, password, displayName, ...rest }: Values) =>
      api<Mailbox>(`/organizations/${orgId}/mailboxes`, {
        method: "POST",
        body: {
          ...rest,
          displayName: displayName || undefined,
          password: password || undefined,
          quotaBytes: quotaMb ? Number(quotaMb) * MB : undefined,
        },
      }),
    onSuccess: (m) => {
      toast.success(`Created ${m.address}`)
      refresh()
      setCreating(false)
      form.reset()
    },
  })
  const toggle = useMutation({
    mutationFn: (m: Mailbox) => api<Mailbox>(`/mailboxes/${m.id}`, { method: "PATCH", body: { active: !m.active } }),
    onSuccess: refresh,
  })
  const remove = useMutation({
    mutationFn: (id: string) => api(`/mailboxes/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Mailbox deleted")
      setDeleting(null)
      refresh()
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Mailboxes"
        description="Each mailbox has an owner account that can sign in with the mailbox address."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            Create mailbox
          </Button>
        }
      />
      <QueryView query={mailboxes} empty={<EmptyState icon={Inbox} title="No mailboxes" description="Create a mailbox on one of this organization's domains." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Display name</TableHead>
                <TableHead>Quota</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((m) => (
                <TableRow key={m.id}>
                  <TableCell className="font-mono text-sm">{m.address}</TableCell>
                  <TableCell>{m.displayName || "—"}</TableCell>
                  <TableCell>{m.quotaBytes ? formatBytes(m.quotaBytes) : "No limit"}</TableCell>
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <Switch
                        checked={m.active}
                        disabled={toggle.isPending && toggle.variables?.id === m.id}
                        onCheckedChange={() => toggle.mutate(m)}
                        aria-label={`${m.active ? "Deactivate" : "Activate"} ${m.address}`}
                      />
                      <StatusBadge tone={m.active ? "success" : "neutral"}>{m.active ? "Active" : "Inactive"}</StatusBadge>
                    </div>
                  </TableCell>
                  <TableCell>
                    <IconButton label={`Delete ${m.address}`} size="icon-sm" onClick={() => setDeleting(m)}>
                      <Trash2 />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={creating}
        onOpenChange={(open) => {
          setCreating(open)
          if (!open) form.reset()
        }}
        title="Create mailbox"
        description="Aether creates or links an owner account with the mailbox address."
        submitLabel="Create mailbox"
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="mb-local" label="Local part" error={errors.localPart?.message}>
            <Input id="mb-local" placeholder="jane" autoComplete="off" spellCheck={false} aria-invalid={!!errors.localPart} {...form.register("localPart")} />
          </Field>
          <DomainSelect id="mb-domain" orgId={orgId} control={form.control} />
        </div>
        <Field id="mb-name" label="Display name" hint="Shown as the sender name. Optional.">
          <Input id="mb-name" autoComplete="off" {...form.register("displayName")} />
        </Field>
        <Field id="mb-password" label="Password" hint="Optional web sign-in password for the owner, at least 12 characters. Leave empty for single sign-on accounts." error={errors.password?.message}>
          <Input id="mb-password" type="password" autoComplete="new-password" aria-invalid={!!errors.password} {...form.register("password")} />
        </Field>
        <Field id="mb-quota" label="Quota in MB" hint="Leave empty for no limit." error={errors.quotaMb?.message}>
          <Input id="mb-quota" inputMode="numeric" autoComplete="off" aria-invalid={!!errors.quotaMb} {...form.register("quotaMb")} />
        </Field>
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Delete ${deleting?.address}?`}
        description="The mailbox stops receiving mail immediately and its app passwords stop working. Stored mail stays on disk until an operator removes it."
        confirmLabel="Delete mailbox"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </section>
  )
}

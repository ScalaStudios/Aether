"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Forward, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { DomainSelect } from "@/components/admin/domain-select"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { domainsQuery } from "@/lib/queries"
import type { Alias } from "@/lib/types"

const schema = z.object({
  domainId: z.string().min(1, "Choose a domain."),
  localPart: z
    .string()
    .trim()
    .toLowerCase()
    .regex(/^([a-z0-9]([a-z0-9._+-]*[a-z0-9])?)?$/, "Use letters, digits, dots, hyphens, plus signs or underscores."),
  destinations: z.array(z.object({ name: z.string().optional(), address: z.email() })).min(1, "Add at least one destination address."),
})

type Values = z.infer<typeof schema>

export function AliasesTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const aliases = useQuery({ queryKey: ["aliases", orgId], queryFn: () => api<Alias[]>(`/organizations/${orgId}/aliases`) })
  const domains = useQuery(domainsQuery(orgId))
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Alias | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { domainId: "", localPart: "", destinations: [] } })

  const domainName = (id: string) => domains.data?.find((d) => d.id === id)?.name ?? ""

  const create = useMutation({
    mutationFn: (values: Values) =>
      api<Alias>(`/organizations/${orgId}/aliases`, {
        method: "POST",
        body: { ...values, destinations: values.destinations.map((d) => d.address) },
      }),
    onSuccess: () => {
      toast.success("Alias created")
      qc.invalidateQueries({ queryKey: ["aliases", orgId] })
      setCreating(false)
      form.reset()
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) => api(`/aliases/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Alias deleted")
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ["aliases", orgId] })
    },
  })

  const label = (a: Alias) => (a.localPart ? a.address : `Any address @${domainName(a.domainId)}`)

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Aliases"
        description="Forward mail for an address to one or more destinations. A catch-all receives mail for addresses that have no mailbox or alias."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            Create alias
          </Button>
        }
      />
      <QueryView query={aliases} empty={<EmptyState icon={Forward} title="No aliases" description="Create an alias to forward an address without a separate mailbox." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Destinations</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((a) => (
                <TableRow key={a.id}>
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <span className="font-mono text-sm">{label(a)}</span>
                      {!a.localPart && <StatusBadge tone="info">Catch-all</StatusBadge>}
                      {!a.active && <StatusBadge tone="neutral">Inactive</StatusBadge>}
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-sm whitespace-normal break-all">{a.destinations.join(", ")}</TableCell>
                  <TableCell>
                    <IconButton label={`Delete ${label(a)}`} size="icon-sm" onClick={() => setDeleting(a)}>
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
        title="Create alias"
        submitLabel="Create alias"
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="alias-local" label="Local part" hint="Leave empty for a catch-all." error={form.formState.errors.localPart?.message}>
            <Input id="alias-local" placeholder="sales" autoComplete="off" spellCheck={false} aria-invalid={!!form.formState.errors.localPart} {...form.register("localPart")} />
          </Field>
          <DomainSelect id="alias-domain" orgId={orgId} control={form.control} />
        </div>
        <Controller
          control={form.control}
          name="destinations"
          render={({ field, fieldState }) => (
            <Field id="alias-destinations" label="Destinations" hint="Press Enter or type a comma after each address." error={fieldState.error?.message}>
              <RecipientInput id="alias-destinations" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" />
            </Field>
          )}
        />
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title="Delete alias?"
        description={deleting ? `Mail to ${label(deleting)} stops being forwarded immediately.` : ""}
        confirmLabel="Delete alias"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </section>
  )
}

"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { FolderSearch, Plus } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDate, plural } from "@/lib/format"
import { casesQuery } from "@/lib/queries"
import type { CaseCriteria, ComplianceCase } from "@/lib/types"

export const caseSchema = z.object({
  name: z.string().trim().min(1, "Name the case.").max(200, "Use at most 200 characters."),
  description: z.string().trim().max(2000, "Use at most 2,000 characters."),
})

export type CaseValues = z.infer<typeof caseSchema>

const emptyCriteria: CaseCriteria = { scope: "organization", mailboxIds: [], domainIds: [], from: "", to: "", subject: "", body: "", attachment: "", since: "", until: "" }

export function CaseFields({ form }: { form: ReturnType<typeof useForm<CaseValues>> }) {
  return (
    <>
      <Field id="case-name" label="Name" error={form.formState.errors.name?.message}>
        <Input id="case-name" autoComplete="off" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
      </Field>
      <Field id="case-description" label="Description" hint="Optional. The matter, request or investigation this case is for." error={form.formState.errors.description?.message}>
        <Input id="case-description" autoComplete="off" aria-invalid={!!form.formState.errors.description} {...form.register("description")} />
      </Field>
    </>
  )
}

export function CasesTab({ orgId, onOpen }: { orgId: string; onOpen: (id: string) => void }) {
  const qc = useQueryClient()
  const cases = useQuery(casesQuery(orgId))
  const [creating, setCreating] = useState(false)
  const form = useForm<CaseValues>({ resolver: zodResolver(caseSchema), defaultValues: { name: "", description: "" } })

  const create = useMutation({
    mutationFn: (values: CaseValues) => api<ComplianceCase>(`/organizations/${orgId}/compliance/cases`, { method: "POST", body: { ...values, criteria: emptyCriteria } }),
    onSuccess: (c) => {
      toast.success(`Created ${c.name}`)
      qc.invalidateQueries({ queryKey: ["compliance-cases", orgId] })
      setCreating(false)
      form.reset()
      onOpen(c.id)
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Cases"
        description="A case holds one saved search and the mailboxes placed on hold for it. Closing a case releases its holds."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            New case
          </Button>
        }
      />
      <QueryView query={cases} empty={<EmptyState icon={FolderSearch} title="No cases yet" description="Create a case to search mailboxes or place them on hold." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Holds</TableHead>
                <TableHead>Created</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((c) => (
                <TableRow key={c.id}>
                  <TableCell>
                    <Button variant="link" onClick={() => onOpen(c.id)}>
                      {c.name}
                    </Button>
                    {c.description && <p className="max-w-[56ch] truncate text-sm text-fg-tertiary">{c.description}</p>}
                  </TableCell>
                  <TableCell>
                    <StatusBadge tone={c.closedAt ? "neutral" : "success"}>{c.closedAt ? "Closed" : "Open"}</StatusBadge>
                  </TableCell>
                  <TableCell>{c.holds ? plural(c.holds, "mailbox", "mailboxes") : "None"}</TableCell>
                  <TableCell>
                    {formatDate(c.createdAt)}
                    {c.createdBy && <span className="text-fg-tertiary"> by {c.createdBy}</span>}
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
        title="New case"
        description="You choose what to search and which mailboxes to hold after creating it."
        submitLabel="Create case"
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
      >
        <CaseFields form={form} />
      </FormDialog>
    </section>
  )
}

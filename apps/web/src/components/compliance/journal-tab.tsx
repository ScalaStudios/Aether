"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Archive, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, ApiError, errorMessage } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { journalRulesQuery } from "@/lib/queries"
import type { JournalRule } from "@/lib/types"

const schema = z.object({
  match: z
    .string()
    .trim()
    .toLowerCase()
    .regex(/^[^@\s]*@[^@\s]+$/, "Enter an address such as person@example.com, or a domain as @example.com."),
  archiveAddress: z.email("Enter the archive’s email address."),
})

type Values = z.infer<typeof schema>

export function JournalTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const rules = useQuery(journalRulesQuery(orgId))
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<JournalRule | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { match: "", archiveAddress: "" } })

  const create = useMutation({
    mutationFn: (values: Values) => api<JournalRule>(`/organizations/${orgId}/journal-rules`, { method: "POST", body: values }),
    meta: { silent: true },
    onSuccess: (rule) => {
      toast.success(`Journaling ${rule.match}`)
      qc.invalidateQueries({ queryKey: ["journal-rules", orgId] })
      setCreating(false)
      form.reset()
    },
    onError: (error) => {
      if (error instanceof ApiError && (error.code === "invalid_match" || error.code === "journal_rule_exists")) form.setError("match", { message: error.message })
      else if (error instanceof ApiError && error.code === "invalid_archive") form.setError("archiveAddress", { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const remove = useMutation({
    mutationFn: (rule: JournalRule) => api(`/journal-rules/${rule.id}`, { method: "DELETE" }),
    onSuccess: (_d, rule) => {
      toast.success(`Stopped journaling ${rule.match}`)
      qc.invalidateQueries({ queryKey: ["journal-rules", orgId] })
      setDeleting(null)
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Journaling"
        description="A copy of every message sent to or from a journaled address or domain is delivered to its archive address, including mail between people in the organization."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            Add rule
          </Button>
        }
      />
      <QueryView query={rules} empty={<EmptyState icon={Archive} title="Nothing is journaled" description="Add a rule to copy a person’s or a whole domain’s mail to an archive." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Journaled</TableHead>
                <TableHead>Archive</TableHead>
                <TableHead>Added</TableHead>
                <TableHead className="w-16">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((rule) => (
                <TableRow key={rule.id}>
                  <TableCell className="font-mono text-sm">{rule.match}</TableCell>
                  <TableCell className="font-mono text-sm">{rule.archiveAddress}</TableCell>
                  <TableCell>
                    {formatDate(rule.createdAt)}
                    {rule.createdBy && <span className="text-fg-tertiary"> by {rule.createdBy}</span>}
                  </TableCell>
                  <TableCell>
                    <IconButton label={`Stop journaling ${rule.match}`} size="icon-sm" onClick={() => setDeleting(rule)}>
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
        title="Add journal rule"
        description="Each address or domain can have one archive."
        submitLabel="Add rule"
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
      >
        <Field id="journal-match" label="Journal" hint="An address of this organization, or @domain for all of its mail." error={form.formState.errors.match?.message}>
          <Input id="journal-match" autoComplete="off" spellCheck={false} placeholder="@example.com" aria-invalid={!!form.formState.errors.match} {...form.register("match")} />
        </Field>
        <Field id="journal-archive" label="Archive address" hint="Any address, inside or outside the organization." error={form.formState.errors.archiveAddress?.message}>
          <Input id="journal-archive" type="email" autoComplete="off" spellCheck={false} aria-invalid={!!form.formState.errors.archiveAddress} {...form.register("archiveAddress")} />
        </Field>
      </FormDialog>
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Stop journaling ${deleting?.match}?`}
        description="New mail is no longer copied to the archive. Copies already delivered stay there."
        confirmLabel="Stop journaling"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </section>
  )
}

"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { BookUser, Pencil, Plus, Trash2 } from "lucide-react"
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
import type { OrgContact } from "@/lib/types"

const schema = z.object({
  name: z.string().trim().min(1, "Enter a name.").max(200, "Use 200 characters or fewer."),
  email: z.email("Enter a valid email address."),
  company: z.string().trim().max(200, "Use 200 characters or fewer."),
  phone: z.string().trim().max(50, "Use 50 characters or fewer."),
})

type Values = z.infer<typeof schema>

const blank: Values = { name: "", email: "", company: "", phone: "" }

const fieldErrors: Record<string, keyof Values> = {
  invalid_name: "name",
  invalid_email: "email",
  contact_exists: "email",
  invalid_company: "company",
  invalid_phone: "phone",
}

export function ContactsTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const key = ["org-contacts", orgId]
  const contacts = useQuery({ queryKey: key, queryFn: () => api<OrgContact[]>(`/organizations/${orgId}/contacts`) })
  const [editing, setEditing] = useState<OrgContact | "new" | null>(null)
  const [deleting, setDeleting] = useState<OrgContact | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: blank })
  const errors = form.formState.errors
  const existing = editing === "new" ? null : editing

  function open(target: OrgContact | "new") {
    form.reset(target === "new" ? blank : { name: target.name, email: target.email, company: target.company, phone: target.phone })
    setEditing(target)
  }

  const save = useMutation({
    mutationFn: (v: Values) =>
      existing
        ? api<OrgContact>(`/organizations/${orgId}/contacts/${existing.id}`, { method: "PUT", body: v })
        : api<OrgContact>(`/organizations/${orgId}/contacts`, { method: "POST", body: v }),
    meta: { silent: true },
    onSuccess: (c) => {
      toast.success(existing ? `Saved ${c.name}` : `Added ${c.name}`)
      qc.invalidateQueries({ queryKey: key })
      setEditing(null)
    },
    onError: (error) => {
      const field = error instanceof ApiError ? fieldErrors[error.code] : undefined
      if (field) form.setError(field, { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const remove = useMutation({
    mutationFn: (c: OrgContact) => api(`/organizations/${orgId}/contacts/${c.id}`, { method: "DELETE" }),
    onSuccess: (_d, c) => {
      toast.success(`Removed ${c.name}`)
      setDeleting(null)
      qc.invalidateQueries({ queryKey: key })
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Contacts"
        description="Outside people and addresses that everyone in this organization finds in the directory, in address suggestions and in synced address books. Add them to distribution lists like any other address."
        actions={
          <Button onClick={() => open("new")}>
            <Plus aria-hidden />
            Add contact
          </Button>
        }
      />
      <QueryView query={contacts} empty={<EmptyState icon={BookUser} title="No organization contacts" description="Add contractors, partners and suppliers once, so nobody has to keep their own copy." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Email</TableHead>
                <TableHead className="max-md:hidden">Company</TableHead>
                <TableHead className="max-md:hidden">Phone</TableHead>
                <TableHead className="w-20">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((c) => (
                <TableRow key={c.id}>
                  <TableCell className="font-medium">{c.name}</TableCell>
                  <TableCell className="font-mono text-sm">{c.email}</TableCell>
                  <TableCell className="max-md:hidden">{c.company || "—"}</TableCell>
                  <TableCell className="max-md:hidden">{c.phone || "—"}</TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      <IconButton label={`Edit ${c.name}`} size="icon-sm" onClick={() => open(c)}>
                        <Pencil />
                      </IconButton>
                      <IconButton label={`Remove ${c.name}`} size="icon-sm" onClick={() => setDeleting(c)}>
                        <Trash2 />
                      </IconButton>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
        title={existing ? "Edit contact" : "Add contact"}
        description="Shown to everyone in this organization."
        submitLabel={existing ? "Save contact" : "Add contact"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
      >
        <Field id="org-contact-name" label="Name" error={errors.name?.message}>
          <Input id="org-contact-name" autoComplete="off" aria-invalid={!!errors.name} {...form.register("name")} />
        </Field>
        <Field id="org-contact-email" label="Email" error={errors.email?.message}>
          <Input id="org-contact-email" type="email" placeholder="name@example.com" autoComplete="off" spellCheck={false} aria-invalid={!!errors.email} {...form.register("email")} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="org-contact-company" label="Company" hint="Optional." error={errors.company?.message}>
            <Input id="org-contact-company" autoComplete="off" aria-invalid={!!errors.company} {...form.register("company")} />
          </Field>
          <Field id="org-contact-phone" label="Phone" hint="Optional." error={errors.phone?.message}>
            <Input id="org-contact-phone" type="tel" autoComplete="off" aria-invalid={!!errors.phone} {...form.register("phone")} />
          </Field>
        </div>
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Remove ${deleting?.name ?? "contact"}?`}
        description="The contact disappears from the directory and from synced address books. Distribution lists that include the address keep it as a member."
        confirmLabel="Remove contact"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </section>
  )
}

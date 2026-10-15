"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { BookUser, Mail, Pencil, Plus, SearchX, Trash2, Users, X } from "lucide-react"
import Link from "next/link"
import { useState } from "react"
import { Controller, useFieldArray, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { PageContainer } from "@/components/page-container"
import { PageHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { SearchInput } from "@/components/search-input"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { ApiError, api } from "@/lib/api"
import type { Contact, ContactInput, Recipient } from "@/lib/types"

const schema = z.object({
  name: z.string().trim().min(1, "Enter a name.").max(200, "Use 200 characters or fewer."),
  emails: z.array(z.object({ name: z.string().optional(), address: z.email() })).max(10, "Add up to 10 email addresses."),
  phones: z.array(z.object({ value: z.string().trim().max(50, "Use 50 characters or fewer.") })).max(10),
  organization: z.string().trim().max(200, "Use 200 characters or fewer."),
  notes: z.string().max(5000, "Use 5,000 characters or fewer."),
})

type Values = z.infer<typeof schema>

function toValues(contact?: Contact): Values {
  return {
    name: contact?.name ?? "",
    emails: (contact?.emails ?? []).map((address) => ({ address })),
    phones: (contact?.phones ?? []).map((value) => ({ value })),
    organization: contact?.organization ?? "",
    notes: contact?.notes ?? "",
  }
}

function toInput(v: Values): ContactInput {
  return {
    name: v.name,
    emails: v.emails.map((a) => a.address),
    phones: v.phones.map((p) => p.value).filter(Boolean),
    organization: v.organization,
    notes: v.notes,
  }
}

function WriteEmail({ address, name }: { address: string; name: string }) {
  return (
    <IconButton label={`Write email to ${name || address}`} size="icon-sm" asChild>
      <Link href={`/mail?compose=${encodeURIComponent(address)}`}>
        <Mail />
      </Link>
    </IconButton>
  )
}

export function ContactsView() {
  const [tab, setTab] = useState("contacts")
  const [editing, setEditing] = useState<Contact | "new" | null>(null)

  return (
    <PageContainer>
      <PageHeader
        title="Contacts"
        description="People you write to, also available on your phone over CardDAV, and everyone with a mailbox in your organizations."
        actions={
          tab === "contacts" && (
            <Button onClick={() => setEditing("new")}>
              <Plus aria-hidden />
              New contact
            </Button>
          )
        }
      />
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="contacts">My contacts</TabsTrigger>
          <TabsTrigger value="directory">Directory</TabsTrigger>
        </TabsList>
        <TabsContent value="contacts">
          <MyContacts editing={editing} onEdit={setEditing} />
        </TabsContent>
        <TabsContent value="directory">
          <Directory />
        </TabsContent>
      </Tabs>
    </PageContainer>
  )
}

function MyContacts({ editing, onEdit }: { editing: Contact | "new" | null; onEdit: (contact: Contact | "new" | null) => void }) {
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 250)
  const contacts = useQuery({
    queryKey: ["contacts", q],
    queryFn: () => api<Contact[]>("/contacts", { query: { q } }),
    placeholderData: keepPreviousData,
  })
  const [deleting, setDeleting] = useState<Contact | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: toValues() })
  const phones = useFieldArray({ control: form.control, name: "phones" })
  const [opened, setOpened] = useState(editing)
  if (editing !== opened) {
    setOpened(editing)
    if (editing) form.reset(toValues(editing === "new" ? undefined : editing))
  }

  const save = useMutation({
    mutationFn: (v: Values) =>
      editing && editing !== "new"
        ? api<Contact>(`/contacts/${encodeURIComponent(editing.id)}`, { method: "PUT", body: toInput(v) })
        : api<Contact>("/contacts", { method: "POST", body: toInput(v) }),
    onSuccess: (contact) => {
      toast.success(editing === "new" ? `${contact.name} added` : "Contact saved")
      onEdit(null)
      qc.invalidateQueries({ queryKey: ["contacts"] })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "changed") qc.invalidateQueries({ queryKey: ["contacts"] })
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) => api(`/contacts/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Contact deleted")
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ["contacts"] })
    },
  })
  const errors = form.formState.errors

  return (
    <div className="flex flex-col gap-6">
      <SearchInput aria-label="Search contacts" placeholder="Search by name or email" value={search} onChange={(e) => setSearch(e.target.value)} className="max-w-md" />
      <QueryView
        query={contacts}
        empty={
          q ? (
            <EmptyState icon={SearchX} title="No matches" description={`No contacts match “${q}”.`} />
          ) : (
            <EmptyState icon={BookUser} title="No contacts yet" description="Add the people you write to. Contacts you add here also appear in the composer and on synced phones." />
          )
        }
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Email</TableHead>
                <TableHead className="max-md:hidden">Organization</TableHead>
                <TableHead className="w-32">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((c) => (
                <TableRow key={c.id}>
                  <TableCell className="font-medium">{c.name}</TableCell>
                  <TableCell className="font-mono text-sm">{c.emails[0] ?? <span className="font-sans text-fg-tertiary">No email</span>}</TableCell>
                  <TableCell className="max-md:hidden">{c.organization}</TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      {c.emails[0] && <WriteEmail address={c.emails[0]} name={c.name} />}
                      <IconButton label={`Edit ${c.name}`} size="icon-sm" onClick={() => onEdit(c)}>
                        <Pencil />
                      </IconButton>
                      <IconButton label={`Delete ${c.name}`} size="icon-sm" onClick={() => setDeleting(c)}>
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
        onOpenChange={(o) => !o && onEdit(null)}
        title={editing === "new" ? "New contact" : "Edit contact"}
        submitLabel={editing === "new" ? "Add contact" : "Save contact"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
      >
        <Field id="contact-name" label="Name" error={errors.name?.message}>
          <Input id="contact-name" autoComplete="off" aria-invalid={!!errors.name} {...form.register("name")} />
        </Field>
        <Controller
          control={form.control}
          name="emails"
          render={({ field, fieldState }) => (
            <Field id="contact-emails" label="Email addresses" hint="Up to 10. Press Enter or comma after each one." error={fieldState.error?.message ?? fieldState.error?.root?.message}>
              <RecipientInput id="contact-emails" value={field.value} onChange={(next) => field.onChange(next.map((a) => ({ address: a.address })))} invalid={!!fieldState.error} placeholder="name@example.com" />
            </Field>
          )}
        />
        <fieldset className="flex flex-col gap-3">
          <legend className="mb-2 text-label text-fg-secondary">Phone numbers</legend>
          {phones.fields.map((p, i) => (
            <div key={p.id} className="flex items-start gap-2">
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <Input type="tel" aria-label={`Phone number ${i + 1}`} autoComplete="off" aria-invalid={!!errors.phones?.[i]?.value} {...form.register(`phones.${i}.value`)} />
                {errors.phones?.[i]?.value && <p role="alert" className="text-sm text-danger">{errors.phones[i]?.value?.message}</p>}
              </div>
              <IconButton type="button" label={`Remove phone number ${i + 1}`} onClick={() => phones.remove(i)}>
                <X />
              </IconButton>
            </div>
          ))}
          <Button type="button" variant="ghost" className="self-start" disabled={phones.fields.length >= 10} onClick={() => phones.append({ value: "" })}>
            <Plus aria-hidden />
            Add phone number
          </Button>
        </fieldset>
        <Field id="contact-org" label="Organization" error={errors.organization?.message}>
          <Input id="contact-org" autoComplete="off" aria-invalid={!!errors.organization} {...form.register("organization")} />
        </Field>
        <Field id="contact-notes" label="Notes" error={errors.notes?.message}>
          <Textarea id="contact-notes" rows={4} aria-invalid={!!errors.notes} {...form.register("notes")} />
        </Field>
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "contact"}?`}
        description="The contact is also removed from phones and apps that sync your contacts. This cannot be undone."
        confirmLabel="Delete contact"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </div>
  )
}

function Directory() {
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 250)
  const directory = useQuery({
    queryKey: ["directory", q],
    queryFn: () => api<Recipient[]>("/directory", { query: { q } }),
    placeholderData: keepPreviousData,
  })

  return (
    <div className="flex flex-col gap-6">
      <SearchInput aria-label="Search the directory" placeholder="Search by name or address" value={search} onChange={(e) => setSearch(e.target.value)} className="max-w-md" />
      <QueryView
        query={directory}
        empty={
          q ? (
            <EmptyState icon={SearchX} title="No matches" description={`Nobody in your organizations matches “${q}”.`} />
          ) : (
            <EmptyState icon={Users} title="The directory is empty" description="Active mailboxes in your organizations are listed here." />
          )
        }
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Address</TableHead>
                <TableHead className="w-16">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((entry) => (
                <TableRow key={entry.address}>
                  <TableCell className="font-medium">{entry.name || <span className="font-normal text-fg-tertiary">No name</span>}</TableCell>
                  <TableCell className="font-mono text-sm">{entry.address}</TableCell>
                  <TableCell>
                    <div className="flex justify-end">
                      <WriteEmail address={entry.address} name={entry.name} />
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>
    </div>
  )
}

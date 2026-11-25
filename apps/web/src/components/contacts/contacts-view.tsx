"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { BookUser, Mail, Pencil, Plus, SearchX, Trash2, Upload, Users, X } from "lucide-react"
import Link from "next/link"
import { useRef, useState } from "react"
import { Controller, useFieldArray, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { AddressBooksDialog } from "@/components/contacts/address-books-dialog"
import { ContactGroups } from "@/components/contacts/contact-groups"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { PageContainer } from "@/components/page-container"
import { PageHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { SearchInput } from "@/components/search-input"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { ApiError, api, errorMessage } from "@/lib/api"
import { plural } from "@/lib/format"
import { resourceTypes } from "@/lib/resources"
import type { AddressBook, Contact, ContactInput, ImportCounts, Recipient } from "@/lib/types"

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

function toInput(v: Values, bookId: string): ContactInput {
  return {
    name: v.name,
    emails: v.emails.map((a) => a.address),
    phones: v.phones.map((p) => p.value).filter(Boolean),
    organization: v.organization,
    notes: v.notes,
    bookId,
  }
}

function writableBookId(books: AddressBook[], bookId: string) {
  if (bookId !== "all") {
    const selected = books.find((book) => book.id === bookId)
    if (selected && selected.access !== "read") return selected.id
  }
  return books.find((book) => book.id === "contacts" && book.access !== "read")?.id ?? books.find((book) => book.access !== "read")?.id ?? "contacts"
}

function bookNote(books: AddressBook[], bookId: string) {
  if (bookId === "all") return null
  const book = books.find((item) => item.id === bookId)
  if (!book) return null
  const who = book.owner?.name || book.owner?.email
  if (book.access === "read") return `${who || "Someone"} shared this address book with you. You can look through it, and it syncs to your phone as view only.`
  if (book.access === "write") return `${who || "Someone"} shared this address book with you, and you can change it.`
  return book.description || null
}

function BookToolbar({ books, bookId, onBook, onManage }: { books: AddressBook[]; bookId: string; onBook: (id: string) => void; onManage: () => void }) {
  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
      <Select value={bookId} onValueChange={onBook}>
        <SelectTrigger aria-label="Address book" className="w-full sm:w-72">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">All address books</SelectItem>
          {books.map((book) => (
            <SelectItem key={book.id} value={book.id}>
              {book.name}
              {book.access === "read" ? " (view only)" : book.access === "write" ? " (can edit)" : ""}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button variant="secondary" onClick={onManage} className="self-start">
        <BookUser aria-hidden />
        Address books
      </Button>
    </div>
  )
}

function ImportContacts({ bookId }: { bookId: string }) {
  const qc = useQueryClient()
  const fileInput = useRef<HTMLInputElement>(null)
  const importFile = useMutation({
    mutationFn: (file: File) => api<ImportCounts>("/contacts/import", { method: "POST", body: file, query: bookId === "all" ? undefined : { book: bookId } }),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["contacts"] })
      qc.invalidateQueries({ queryKey: ["contact-groups"] })
      toast.success(`Imported ${plural(r.imported + r.updated, "contact")}`, { description: r.skipped ? `Skipped ${plural(r.skipped, "contact")} that couldn't be read.` : undefined })
    },
  })

  return (
    <>
      <input
        ref={fileInput}
        type="file"
        accept=".vcf,text/vcard"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0]
          if (file) importFile.mutate(file)
          e.target.value = ""
        }}
      />
      <Button variant="secondary" loading={importFile.isPending} onClick={() => fileInput.current?.click()}>
        <Upload aria-hidden />
        Import .vcf file
      </Button>
    </>
  )
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
  const [bookId, setBookId] = useState("all")
  const [booksOpen, setBooksOpen] = useState(false)
  const books = useQuery({ queryKey: ["address-books"], queryFn: () => api<AddressBook[]>("/contacts/books") })
  const list = books.data ?? []
  const selected = bookId === "all" ? undefined : list.find((book) => book.id === bookId)
  const canWrite = !selected || selected.access !== "read"
  const createBookId = writableBookId(list, bookId)

  return (
    <PageContainer>
      <PageHeader
        title="Contacts"
        description="Address books you can share, groups you can write to at once, and everyone with a mailbox in your organizations. Contacts sync to phones over CardDAV."
        actions={
          tab === "contacts" && canWrite ? (
            <>
              <ImportContacts bookId={createBookId === "contacts" && bookId === "all" ? "all" : createBookId} />
              <Button onClick={() => setEditing("new")}>
                <Plus aria-hidden />
                New contact
              </Button>
            </>
          ) : undefined
        }
      />
      {books.isError && <p role="alert" className="text-sm text-danger">{errorMessage(books.error)}</p>}
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="contacts">My contacts</TabsTrigger>
          <TabsTrigger value="groups">Groups</TabsTrigger>
          <TabsTrigger value="directory">Directory</TabsTrigger>
        </TabsList>
        <TabsContent value="contacts" className="flex flex-col gap-6">
          <BookToolbar books={list} bookId={bookId} onBook={setBookId} onManage={() => setBooksOpen(true)} />
          {bookNote(list, bookId) && <p className="text-sm text-fg-secondary">{bookNote(list, bookId)}</p>}
          <MyContacts editing={editing} onEdit={setEditing} bookId={bookId} books={list} canWrite={canWrite} createBookId={createBookId} />
        </TabsContent>
        <TabsContent value="groups" className="flex flex-col gap-6">
          <BookToolbar books={list} bookId={bookId} onBook={setBookId} onManage={() => setBooksOpen(true)} />
          {bookNote(list, bookId) && <p className="text-sm text-fg-secondary">{bookNote(list, bookId)}</p>}
          <ContactGroups bookId={bookId} books={list} canWrite={canWrite} createBookId={createBookId} />
        </TabsContent>
        <TabsContent value="directory">
          <Directory />
        </TabsContent>
      </Tabs>
      <AddressBooksDialog open={booksOpen} onOpenChange={setBooksOpen} books={list} />
    </PageContainer>
  )
}

function MyContacts({ editing, onEdit, bookId, books, canWrite, createBookId }: { editing: Contact | "new" | null; onEdit: (contact: Contact | "new" | null) => void; bookId: string; books: AddressBook[]; canWrite: boolean; createBookId: string }) {
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 250)
  const contacts = useQuery({
    queryKey: ["contacts", bookId, q],
    queryFn: () => api<Contact[]>("/contacts", { query: { q, book: bookId === "all" ? undefined : bookId } }),
    placeholderData: keepPreviousData,
  })
  const [deleting, setDeleting] = useState<Contact | null>(null)
  const [targetBook, setTargetBook] = useState(createBookId)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: toValues() })
  const phones = useFieldArray({ control: form.control, name: "phones" })
  const [opened, setOpened] = useState(editing)
  if (editing !== opened) {
    setOpened(editing)
    if (editing) {
      form.reset(toValues(editing === "new" ? undefined : editing))
      setTargetBook(editing === "new" ? createBookId : editing.bookId || createBookId)
    }
  }
  const names = new Map(books.map((book) => [book.id, book.name]))
  const showBook = bookId === "all" && books.length > 1
  const writable = books.filter((book) => book.access !== "read")
  const savingBook = editing && editing !== "new" ? editing.bookId || createBookId : targetBook

  const save = useMutation({
    mutationFn: (v: Values) =>
      editing && editing !== "new"
        ? api<Contact>(`/contacts/${encodeURIComponent(editing.id)}`, { method: "PUT", body: toInput(v, editing.bookId || createBookId) })
        : api<Contact>("/contacts", { method: "POST", body: toInput(v, targetBook) }),
    onSuccess: (contact) => {
      toast.success(editing === "new" ? `${contact.name} added` : "Contact saved")
      onEdit(null)
      qc.invalidateQueries({ queryKey: ["contacts"] })
      qc.invalidateQueries({ queryKey: ["recipients"] })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "changed") qc.invalidateQueries({ queryKey: ["contacts"] })
    },
  })
  const remove = useMutation({
    mutationFn: (contact: Contact) => api(`/contacts/${encodeURIComponent(contact.id)}`, { method: "DELETE", query: { book: contact.bookId } }),
    onSuccess: () => {
      toast.success("Contact deleted")
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ["contacts"] })
      qc.invalidateQueries({ queryKey: ["contact-groups"] })
      qc.invalidateQueries({ queryKey: ["recipients"] })
    },
  })
  const errors = form.formState.errors
  const destination = names.get(savingBook) || "Contacts"

  return (
    <div className="flex flex-col gap-6">
      <SearchInput aria-label="Search contacts" placeholder="Search by name or email" value={search} onChange={(e) => setSearch(e.target.value)} className="max-w-md" />
      <QueryView
        query={contacts}
        empty={
          q ? (
            <EmptyState icon={SearchX} title="No matches" description={`No contacts match “${q}”.`} />
          ) : (
            <EmptyState icon={BookUser} title="No contacts yet" description="Add the people you write to. Contacts you add here also appear in the composer and on synced phones." action={canWrite ? <Button onClick={() => onEdit("new")}><Plus aria-hidden />New contact</Button> : undefined} />
          )
        }
      >
        {(items) => (
          <Table className="table-fixed">
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead className="max-sm:hidden">Email</TableHead>
                <TableHead className="max-md:hidden">Organization</TableHead>
                {showBook && <TableHead className="max-lg:hidden">Address book</TableHead>}
                <TableHead className="w-44">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((c) => (
                <TableRow key={`${c.bookId}/${c.id}`}>
                  <TableCell className="font-medium">
                    <span className="inline-flex max-w-full flex-wrap items-center gap-2">
                      <span className="min-w-0 truncate">{c.name}</span>
                      {c.readOnly && <span className="rounded-full bg-muted px-2 py-0.5 text-xs font-semibold text-fg-secondary">View only</span>}
                    </span>
                    <span className="block truncate font-mono text-sm font-normal text-fg-tertiary sm:hidden">{c.emails[0] ?? "No email"}</span>
                    {showBook && <span className="block truncate text-sm font-normal text-fg-tertiary lg:hidden">{names.get(c.bookId ?? "")}</span>}
                  </TableCell>
                  <TableCell className="hidden font-mono text-sm sm:table-cell">
                    <span className="block truncate">{c.emails[0] ?? <span className="font-sans text-fg-tertiary">No email</span>}</span>
                  </TableCell>
                  <TableCell className="max-md:hidden">
                    <span className="block truncate">{c.organization}</span>
                  </TableCell>
                  {showBook && <TableCell className="max-lg:hidden">{names.get(c.bookId ?? "")}</TableCell>}
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      {c.emails[0] && <WriteEmail address={c.emails[0]} name={c.name} />}
                      {!c.readOnly && (
                        <IconButton label={`Edit ${c.name}`} size="icon-sm" onClick={() => onEdit(c)}>
                          <Pencil />
                        </IconButton>
                      )}
                      {!c.readOnly && (
                        <IconButton label={`Delete ${c.name}`} size="icon-sm" onClick={() => setDeleting(c)}>
                          <Trash2 />
                        </IconButton>
                      )}
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
        description={editing === "new" ? `This contact is saved in ${destination}.` : undefined}
        submitLabel={editing === "new" ? "Add contact" : "Save contact"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
      >
        {editing === "new" && writable.length > 1 && bookId === "all" && (
          <Field id="contact-book" label="Address book">
            <Select value={targetBook} onValueChange={setTargetBook}>
              <SelectTrigger id="contact-book" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {writable.map((book) => (
                  <SelectItem key={book.id} value={book.id}>
                    {book.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        )}
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
        description="The contact is also removed from phones and apps that sync this address book, and from any groups they were in. This cannot be undone."
        confirmLabel="Delete contact"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
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
            <EmptyState icon={Users} title="The directory is empty" description="Mailboxes, lists and organization contacts in your organizations are listed here." />
          )
        }
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Address</TableHead>
                <TableHead className="max-md:hidden">Company</TableHead>
                <TableHead className="max-lg:hidden">Phone</TableHead>
                <TableHead className="w-16">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((entry) => (
                <TableRow key={entry.address}>
                  <TableCell className="font-medium">
                    <span className="inline-flex items-center gap-2">
                      {entry.name || <span className="font-normal text-fg-tertiary">No name</span>}
                      {entry.kind && <StatusBadge tone="neutral">{resourceTypes[entry.kind].label}</StatusBadge>}
                    </span>
                  </TableCell>
                  <TableCell className="font-mono text-sm">{entry.address}</TableCell>
                  <TableCell className="max-md:hidden">{entry.company}</TableCell>
                  <TableCell className="max-lg:hidden">{entry.phone}</TableCell>
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

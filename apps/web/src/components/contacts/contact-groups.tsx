"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Mail, Pencil, Plus, SearchX, Trash2, UsersRound, X } from "lucide-react"
import Link from "next/link"
import { useState } from "react"
import { useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SearchInput } from "@/components/search-input"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { ApiError, api } from "@/lib/api"
import type { AddressBook, Contact, ContactGroup, ContactGroupMember } from "@/lib/types"

function people(count: number) {
  return count === 1 ? "1 person" : `${count} people`
}

const schema = z.object({
  name: z.string().trim().min(1, "Enter a name.").max(200, "Use 200 characters or fewer."),
  bookId: z.string().min(1, "Choose an address book."),
  members: z.array(z.object({ id: z.string(), name: z.string(), emails: z.array(z.string()) })).max(500, "A group can have up to 500 people."),
})

type Values = z.infer<typeof schema>

function memberLine(group: ContactGroup) {
  if (group.members.length === 0) return "No one yet"
  const names = group.members.map((member) => member.name || "Unnamed")
  const shown = names.slice(0, 3).join(", ")
  return names.length > 3 ? `${shown}, and ${names.length - 3} more` : shown
}

export function ContactGroups({ bookId, books, canWrite, createBookId }: { bookId: string; books: AddressBook[]; canWrite: boolean; createBookId: string }) {
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 250)
  const groups = useQuery({
    queryKey: ["contact-groups", bookId, q],
    queryFn: () => api<ContactGroup[]>("/contacts/groups", { query: { q, book: bookId === "all" ? undefined : bookId } }),
    placeholderData: keepPreviousData,
  })
  const [editing, setEditing] = useState<ContactGroup | "new" | null>(null)
  const [deleting, setDeleting] = useState<ContactGroup | null>(null)
  const names = new Map(books.map((book) => [book.id, book.name]))
  const showBook = bookId === "all" && books.length > 1
  const writable = books.filter((book) => book.access !== "read")

  const remove = useMutation({
    mutationFn: (group: ContactGroup) => api(`/contacts/groups/${encodeURIComponent(group.id)}`, { method: "DELETE", query: { book: group.bookId } }),
    onSuccess: () => {
      toast.success("Group deleted")
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ["contact-groups"] })
    },
  })

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <SearchInput aria-label="Search groups" placeholder="Search by group name" value={search} onChange={(e) => setSearch(e.target.value)} className="w-full max-w-md" />
        {canWrite && (
          <Button onClick={() => setEditing("new")} className="self-start">
            <Plus aria-hidden />
            New group
          </Button>
        )}
      </div>
      <QueryView
        query={groups}
        empty={
          q ? (
            <EmptyState icon={SearchX} title="No matches" description={`No groups match “${q}”.`} />
          ) : (
            <EmptyState icon={UsersRound} title="No groups yet" description="Group people you write to often, such as suppliers or a board, and message all of them at once." action={canWrite ? <Button onClick={() => setEditing("new")}><Plus aria-hidden />New group</Button> : undefined} />
          )
        }
      >
        {(items) => (
          <Table className="table-fixed">
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead className="max-sm:hidden">People</TableHead>
                {showBook && <TableHead className="hidden w-[22%] md:table-cell">Address book</TableHead>}
                <TableHead className="w-44">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((group) => {
                const emails = group.members.flatMap((member) => member.emails)
                return (
                  <TableRow key={`${group.bookId}/${group.id}`}>
                    <TableCell className="font-medium">
                      <span className="block truncate">{group.name}</span>
                      <span className="block truncate text-sm font-normal text-fg-tertiary sm:hidden">{memberLine(group)} · {people(group.members.length)}</span>
                      {showBook && <span className="block truncate text-sm font-normal text-fg-tertiary md:hidden">{names.get(group.bookId)}</span>}
                    </TableCell>
                    <TableCell className="hidden sm:table-cell">
                      <span className="block truncate">{memberLine(group)}</span>
                      <span className="text-sm text-fg-tertiary">{people(group.members.length)}</span>
                    </TableCell>
                    {showBook && <TableCell className="hidden truncate md:table-cell">{names.get(group.bookId)}</TableCell>}
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        {emails.length > 0 && (
                          <IconButton label={`Write email to ${group.name}`} size="icon-sm" asChild>
                            <Link href={`/mail?group=${encodeURIComponent(`${group.bookId}/${group.id}`)}`}>
                              <Mail />
                            </Link>
                          </IconButton>
                        )}
                        {!group.readOnly && (
                          <IconButton label={`Edit ${group.name}`} size="icon-sm" onClick={() => setEditing(group)}>
                            <Pencil />
                          </IconButton>
                        )}
                        {!group.readOnly && (
                          <IconButton label={`Delete ${group.name}`} size="icon-sm" onClick={() => setDeleting(group)}>
                            <Trash2 />
                          </IconButton>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </QueryView>
      <GroupDialog
        editing={editing}
        books={writable}
        initialBookId={editing && editing !== "new" ? editing.bookId : createBookId}
        lockBook={editing !== "new" && editing !== null}
        onClose={() => setEditing(null)}
      />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(next) => !next && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "group"}?`}
        description="The people stay in the address book. The group is also removed from phones and apps that sync it. This cannot be undone."
        confirmLabel="Delete group"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </div>
  )
}

function GroupDialog({ editing, books, initialBookId, lockBook, onClose }: { editing: ContactGroup | "new" | null; books: AddressBook[]; initialBookId: string; lockBook: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { name: "", bookId: initialBookId, members: [] } })
  const [opened, setOpened] = useState(editing)
  if (editing !== opened) {
    setOpened(editing)
    if (editing) {
      form.reset(editing === "new" ? { name: "", bookId: initialBookId, members: [] } : { name: editing.name, bookId: editing.bookId, members: editing.members })
    }
  }
  const bookId = useWatch({ control: form.control, name: "bookId" })
  const members = useWatch({ control: form.control, name: "members" }) ?? []
  const [memberSearch, setMemberSearch] = useState("")
  const q = useDebouncedValue(memberSearch.trim(), 250)
  const contacts = useQuery({
    queryKey: ["contacts", bookId, q, "group-picker"],
    queryFn: () => api<Contact[]>("/contacts", { query: { q, book: bookId } }),
    enabled: editing !== null && !!bookId,
  })
  const chosen = new Set(members.map((member) => member.id))
  const results = (contacts.data ?? []).filter((contact) => !chosen.has(contact.id)).slice(0, 8)
  const bookName = books.find((book) => book.id === bookId)?.name

  const save = useMutation({
    mutationFn: (values: Values) => {
      const body = { name: values.name, bookId: values.bookId, members: values.members.map((member) => member.id) }
      return editing && editing !== "new"
        ? api<ContactGroup>(`/contacts/groups/${encodeURIComponent(editing.id)}`, { method: "PUT", body })
        : api<ContactGroup>("/contacts/groups", { method: "POST", body })
    },
    onSuccess: (group) => {
      toast.success(editing === "new" ? `${group.name} added` : "Group saved")
      setMemberSearch("")
      onClose()
      qc.invalidateQueries({ queryKey: ["contact-groups"] })
      qc.invalidateQueries({ queryKey: ["recipients"] })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "changed") qc.invalidateQueries({ queryKey: ["contact-groups"] })
    },
  })

  function add(contact: Contact) {
    const member: ContactGroupMember = { id: contact.id, name: contact.name, emails: contact.emails }
    form.setValue("members", [...members, member], { shouldValidate: true })
    setMemberSearch("")
  }

  const errors = form.formState.errors

  return (
    <FormDialog
      open={editing !== null}
      onOpenChange={(next) => {
        if (!next) {
          setMemberSearch("")
          onClose()
        }
      }}
      title={editing === "new" ? "New group" : "Edit group"}
      description={bookName ? `People in ${bookName}. Writing to the group addresses everyone here who has an email.` : "Choose people from one address book."}
      submitLabel={editing === "new" ? "Add group" : "Save group"}
      pending={save.isPending}
      onSubmit={form.handleSubmit((values) => save.mutate(values))}
    >
      <Field id="group-name" label="Name" error={errors.name?.message}>
        <Input id="group-name" autoComplete="off" aria-invalid={!!errors.name} {...form.register("name")} />
      </Field>
      {!lockBook && books.length > 1 && (
        <Field id="group-book" label="Address book" error={errors.bookId?.message}>
          <Select
            value={bookId}
            onValueChange={(value) => {
              form.setValue("bookId", value, { shouldValidate: true })
              form.setValue("members", [])
              setMemberSearch("")
            }}
          >
            <SelectTrigger id="group-book" aria-label="Address book" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {books.map((book) => (
                <SelectItem key={book.id} value={book.id}>
                  {book.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
      <fieldset className="flex flex-col gap-3">
        <legend className="text-label text-fg-secondary">People</legend>
        <p className="text-sm text-fg-tertiary">{members.length ? people(members.length) : "Search this address book and add the people who belong in the group."}</p>
        {errors.members?.message && <p role="alert" className="text-sm text-danger">{errors.members.message}</p>}
        {members.length > 0 && (
          <ul className="flex max-h-40 flex-col gap-1 overflow-y-auto">
            {members.map((member, index) => (
              <li key={member.id} className="flex min-h-11 items-center justify-between gap-2 rounded-md bg-sunken px-3">
                <span className="min-w-0 truncate">{member.name || "Unnamed"}</span>
                <IconButton type="button" label={`Remove ${member.name || "person"}`} size="icon-sm" onClick={() => form.setValue("members", members.filter((_, i) => i !== index))}>
                  <X />
                </IconButton>
              </li>
            ))}
          </ul>
        )}
        <Input
          aria-label="Search people to add"
          placeholder="Search by name or email"
          value={memberSearch}
          onChange={(e) => setMemberSearch(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault()
              if (results[0] && members.length < 500) add(results[0])
            }
          }}
        />
        {results.length > 0 && (
          <ul className="flex max-h-48 flex-col overflow-y-auto rounded-lg border border-border" aria-label="People you can add">
            {results.map((contact) => (
              <li key={contact.id}>
                <button type="button" className="flex min-h-11 w-full cursor-pointer flex-col items-start gap-0.5 px-3 py-2 text-left hover:bg-muted sm:flex-row sm:items-center sm:gap-3" disabled={members.length >= 500} onClick={() => add(contact)}>
                  <span className="min-w-0 max-w-full truncate sm:flex-1">{contact.name}</span>
                  <span className="max-w-full truncate font-mono text-sm text-fg-tertiary">{contact.emails[0] ?? "No email"}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
        {memberSearch.trim() && contacts.isSuccess && results.length === 0 && <p className="text-sm text-fg-tertiary">No one in this address book matches that search.</p>}
      </fieldset>
    </FormDialog>
  )
}

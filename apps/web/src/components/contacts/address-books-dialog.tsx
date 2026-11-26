"use client"

import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Trash2, UserPlus, X } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { IconButton } from "@/components/icon-button"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useSession } from "@/hooks/use-session"
import { api, errorMessage } from "@/lib/api"
import type { AddressBook } from "@/lib/types"

const palette = ["#3b82f6", "#10b981", "#f59e0b", "#e11d48", "#0ea5e9", "#64748b"]

const accessLabels = { owner: "Yours", write: "Can edit", read: "View only" }

function Swatch({ color }: { color: string }) {
  return <span aria-hidden className="size-3 shrink-0 rounded-full" style={{ background: color || "var(--color-ember)" }} />
}

export function AddressBooksDialog({ open, onOpenChange, books }: { open: boolean; onOpenChange: (open: boolean) => void; books: AddressBook[] }) {
  const qc = useQueryClient()
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [color, setColor] = useState(palette[0])
  const [sharing, setSharing] = useState<string | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<AddressBook | null>(null)
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["address-books"] })
    qc.invalidateQueries({ queryKey: ["contacts"] })
    qc.invalidateQueries({ queryKey: ["contact-groups"] })
  }

  const create = useMutation({
    mutationFn: () => api<AddressBook>("/contacts/books", { method: "POST", body: { name, description, color } }),
    onSuccess: () => {
      setName("")
      setDescription("")
      refresh()
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const remove = useMutation({
    mutationFn: (id: string) => api(`/contacts/books/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      setDeleting(null)
      refresh()
    },
    onError: (error) => toast.error(errorMessage(error)),
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>Address books</DialogTitle>
          <DialogDescription>Create an address book for a team or a set of customers, and share it with people in your organization.</DialogDescription>
        </DialogHeader>
        <ul className="flex flex-col divide-y divide-border">
          {books.map((book) => (
            <li key={book.id} className="flex flex-col gap-3 py-3">
              <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
                <div className="flex min-w-0 flex-1 items-center gap-3">
                  <Swatch color={book.color} />
                  <div className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate font-semibold">{book.name}</span>
                    <span className="truncate text-sm text-fg-tertiary">
                      {book.owner ? `${book.owner.name || book.owner.email} · ${accessLabels[book.access]}` : book.description || (book.shares?.length ? `Shared with ${book.shares.length}` : "Only you")}
                    </span>
                  </div>
                </div>
                {book.access === "owner" && (
                  <div className="flex flex-wrap items-center gap-2">
                    <Button size="sm" variant="secondary" aria-expanded={sharing === book.id} onClick={() => { setEditing(null); setSharing(sharing === book.id ? null : book.id) }}>
                      <UserPlus aria-hidden />
                      Share
                    </Button>
                    <Button size="sm" variant="secondary" aria-expanded={editing === book.id} onClick={() => { setSharing(null); setEditing(editing === book.id ? null : book.id) }}>
                      Edit
                    </Button>
                    {book.id !== "contacts" && (
                      <IconButton label={`Delete ${book.name}`} size="icon-sm" onClick={() => setDeleting(book)}>
                        <Trash2 />
                      </IconButton>
                    )}
                  </div>
                )}
              </div>
              {sharing === book.id && <SharePanel book={book} onChanged={refresh} />}
              {editing === book.id && <EditPanel book={book} onChanged={refresh} onClose={() => setEditing(null)} />}
            </li>
          ))}
        </ul>
        <form
          className="flex flex-col gap-3 border-t border-border pt-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (name.trim()) create.mutate()
          }}
        >
          <label htmlFor="new-address-book" className="text-label">
            New address book
          </label>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input id="new-address-book" value={name} maxLength={100} placeholder="Sales, Vendors, Board…" onChange={(e) => setName(e.target.value)} />
            <Button type="submit" variant="secondary" loading={create.isPending} disabled={!name.trim()} className="shrink-0">
              Create
            </Button>
          </div>
          <Textarea aria-label="Description" value={description} maxLength={500} rows={2} placeholder="Who these contacts are for" onChange={(e) => setDescription(e.target.value)} />
          <div role="radiogroup" aria-label="Color" className="flex gap-2">
            {palette.map((swatch) => (
              <button
                key={swatch}
                type="button"
                role="radio"
                aria-checked={color === swatch}
                aria-label={swatch}
                onClick={() => setColor(swatch)}
                className={cn("size-8 rounded-full border-2 border-transparent transition-colors duration-160", color === swatch && "border-foreground")}
                style={{ background: swatch, backgroundClip: "content-box", padding: 4 }}
              />
            ))}
          </div>
        </form>
        <ConfirmDialog
          open={!!deleting}
          onOpenChange={(next) => !next && setDeleting(null)}
          title={`Delete “${deleting?.name ?? ""}”?`}
          description="Every contact and group in this address book is deleted, and people it was shared with lose access. This cannot be undone."
          confirmLabel="Delete address book"
          pending={remove.isPending}
          onConfirm={() => deleting && remove.mutate(deleting.id)}
        />
      </DialogContent>
    </Dialog>
  )
}

function EditPanel({ book, onChanged, onClose }: { book: AddressBook; onChanged: () => void; onClose: () => void }) {
  const [name, setName] = useState(book.name)
  const [description, setDescription] = useState(book.description)
  const [color, setColor] = useState(book.color)
  const save = useMutation({
    mutationFn: () => api(`/contacts/books/${encodeURIComponent(book.id)}`, { method: "PATCH", body: { name, description, color } }),
    onSuccess: () => {
      onChanged()
      onClose()
    },
    onError: (error) => toast.error(errorMessage(error)),
  })

  return (
    <form
      className="flex flex-col gap-3 rounded-lg bg-sunken p-3"
      onSubmit={(e) => {
        e.preventDefault()
        if (name.trim()) save.mutate()
      }}
    >
      <Input aria-label="Name" value={name} maxLength={100} onChange={(e) => setName(e.target.value)} />
      <Textarea aria-label="Description" value={description} maxLength={500} rows={2} onChange={(e) => setDescription(e.target.value)} />
      <div className="flex flex-wrap items-center gap-2">
        <div role="radiogroup" aria-label="Color" className="flex gap-2">
          {palette.map((swatch) => (
            <button
              key={swatch}
              type="button"
              role="radio"
              aria-checked={color === swatch}
              aria-label={swatch}
              onClick={() => setColor(swatch)}
              className={cn("size-8 rounded-full border-2 border-transparent transition-colors duration-160", color === swatch && "border-foreground")}
              style={{ background: swatch, backgroundClip: "content-box", padding: 4 }}
            />
          ))}
        </div>
        <Button type="button" size="sm" variant={color === "" ? "secondary" : "quiet"} aria-pressed={color === ""} onClick={() => setColor("")}>
          Default color
        </Button>
      </div>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" loading={save.isPending} disabled={!name.trim()}>
          Save
        </Button>
      </div>
    </form>
  )
}

function SharePanel({ book, onChanged }: { book: AddressBook; onChanged: () => void }) {
  const session = useSession()
  const adminOrgs = session.data?.memberships.filter((membership) => membership.role !== "member") ?? []
  const [target, setTarget] = useState("")
  const [access, setAccess] = useState<"read" | "write">("read")
  const orgId = target.startsWith("org:") ? target.slice(4) : ""

  const share = useMutation({
    mutationFn: () => api(`/contacts/books/${encodeURIComponent(book.id)}/shares`, { method: "POST", body: orgId ? { orgId, access } : { email: target.trim(), access } }),
    onSuccess: () => {
      setTarget("")
      onChanged()
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const unshare = useMutation({
    mutationFn: (id: string) => api(`/contacts/books/${encodeURIComponent(book.id)}/shares/${id}`, { method: "DELETE" }),
    onSuccess: onChanged,
    onError: (error) => toast.error(errorMessage(error)),
  })

  return (
    <div className="flex flex-col gap-3 rounded-lg bg-sunken p-3">
      {book.shares && book.shares.length > 0 && (
        <ul className="flex flex-col gap-1 text-sm">
          {book.shares.map((item) => (
            <li key={item.id} className="flex items-center justify-between gap-3">
              <span className="truncate">{item.orgName ? `Everyone in ${item.orgName}` : item.name || item.email}</span>
              <span className="flex items-center gap-2">
                <span className="text-fg-tertiary">{accessLabels[item.access]}</span>
                <IconButton label={`Stop sharing with ${item.orgName || item.name || item.email}`} size="icon-sm" onClick={() => unshare.mutate(item.id)} disabled={unshare.isPending}>
                  <X />
                </IconButton>
              </span>
            </li>
          ))}
        </ul>
      )}
      <form
        className="flex flex-col gap-2 sm:flex-row sm:flex-wrap"
        onSubmit={(e) => {
          e.preventDefault()
          if (target.trim()) share.mutate()
        }}
      >
        {orgId ? (
          <Button type="button" variant="secondary" className="min-w-0 flex-1 justify-between" onClick={() => setTarget("")}>
            <span className="truncate">Everyone in {adminOrgs.find((org) => org.orgId === orgId)?.orgName}</span>
            <X aria-hidden />
          </Button>
        ) : (
          <Input aria-label="Email address" type="email" placeholder="name@company.com" className="min-w-0 flex-1" value={target} onChange={(e) => setTarget(e.target.value)} />
        )}
        <Select value={access} onValueChange={(value) => setAccess(value as "read" | "write")}>
          <SelectTrigger aria-label="Access" className="w-full sm:w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="read">View only</SelectItem>
            <SelectItem value="write">Can edit</SelectItem>
          </SelectContent>
        </Select>
        <Button type="submit" loading={share.isPending} disabled={!target.trim()}>
          Share
        </Button>
      </form>
      {!orgId && adminOrgs.length > 0 && (
        <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap">
          {adminOrgs.map((org) => (
            <Button key={org.orgId} type="button" size="sm" variant="quiet" onClick={() => setTarget(`org:${org.orgId}`)}>
              Share with everyone in {org.orgName}
            </Button>
          ))}
        </div>
      )}
    </div>
  )
}

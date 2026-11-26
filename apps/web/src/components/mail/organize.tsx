"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Bookmark, Ellipsis, Pencil, Tag, Trash2 } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { LabelDialog, LabelDot, SearchDialog } from "@/components/mail/label-controls"
import { RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { api, errorMessage } from "@/lib/api"
import { savedSearchBody } from "@/lib/mail"
import { labelsQuery, savedSearchesQuery } from "@/lib/queries"
import type { Label, Mailbox, SavedSearch } from "@/lib/types"

function OrganizeRow({
  label,
  active,
  swatch,
  icon,
  editable,
  menuLabel,
  editLabel,
  onOpen,
  onEdit,
  onDelete,
}: {
  label: string
  active: boolean
  swatch?: string
  icon?: React.ReactNode
  editable: boolean
  menuLabel: string
  editLabel: string
  onOpen: () => void
  onEdit: () => void
  onDelete: () => void
}) {
  return (
    <div className={cn("relative rounded-lg", editable && "group/organize")}>
      <button
        type="button"
        aria-current={active ? "page" : undefined}
        onClick={onOpen}
        className={cn(
          "flex min-h-10 w-full cursor-pointer items-center gap-3 rounded-lg px-3 text-left text-[15px] font-medium text-fg-secondary transition-colors duration-160 hover:bg-muted hover:text-foreground max-md:min-h-11",
          active && "bg-muted text-foreground",
          editable && "max-md:pr-14"
        )}
      >
        {swatch ? <LabelDot color={swatch} /> : icon}
        <span className="flex-1 truncate">{label}</span>
      </button>
      {editable && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="quiet"
              size="icon-sm"
              aria-label={menuLabel}
              className="absolute top-1/2 right-1 -translate-y-1/2 md:bg-muted md:opacity-0 md:group-hover/organize:opacity-100 md:group-has-[:focus-visible]/organize:opacity-100 md:aria-expanded:opacity-100"
            >
              <Ellipsis />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-48">
            <DropdownMenuItem onSelect={onEdit}>
              <Pencil aria-hidden />
              {editLabel}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={onDelete}>
              <Trash2 aria-hidden />
              Delete…
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  )
}

export function MailOrganize({
  mailbox,
  active,
  onLabel,
  onSearch,
}: {
  mailbox: Mailbox
  active: { kind: "label" | "search"; id: string } | null
  onLabel: (id: string) => void
  onSearch: (id: string) => void
}) {
  const qc = useQueryClient()
  const editable = mailbox.access !== "read"
  const labels = useQuery({ ...labelsQuery(mailbox.id), enabled: true })
  const searches = useQuery(savedSearchesQuery(mailbox.id))
  const [labelDialog, setLabelDialog] = useState<Label | "new" | null>(null)
  const [renameSearch, setRenameSearch] = useState<SavedSearch | null>(null)
  const [deleteLabel, setDeleteLabel] = useState<Label | null>(null)
  const [deleteSearch, setDeleteSearch] = useState<SavedSearch | null>(null)
  const [error, setError] = useState("")

  const removeLabel = useMutation({
    meta: { silent: true },
    mutationFn: (label: Label) => api(`/mailboxes/${mailbox.id}/labels/${label.id}`, { method: "DELETE" }),
    onSuccess: (_data, label) => {
      toast.success(`Deleted ${label.name}`)
      setDeleteLabel(null)
      qc.invalidateQueries({ queryKey: ["labels", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["saved-searches", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["rules", mailbox.id] })
    },
    onError: (e) => setError(errorMessage(e)),
  })

  const removeSearch = useMutation({
    meta: { silent: true },
    mutationFn: (search: SavedSearch) => api(`/mailboxes/${mailbox.id}/saved-searches/${search.id}`, { method: "DELETE" }),
    onSuccess: (_data, search) => {
      toast.success(`Deleted ${search.name}`)
      setDeleteSearch(null)
      qc.invalidateQueries({ queryKey: ["saved-searches", mailbox.id] })
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  const rename = useMutation({
    meta: { silent: true },
    mutationFn: ({ search, name }: { search: SavedSearch; name: string }) =>
      api<SavedSearch>(`/mailboxes/${mailbox.id}/saved-searches/${search.id}`, { method: "PATCH", body: savedSearchBody({ ...search, name }) }),
    onSuccess: (saved) => {
      toast.success(`Renamed to ${saved.name}`)
      setRenameSearch(null)
      setError("")
      qc.invalidateQueries({ queryKey: ["saved-searches", mailbox.id] })
    },
    onError: (e) => setError(errorMessage(e)),
  })

  if (!editable && !labels.data?.length && !searches.data?.length && !labels.isPending && !searches.isPending) return null
  if (labels.isPending || searches.isPending) return <RowsSkeleton rows={2} />

  const failed = labels.isError || searches.isError

  return (
    <div className="flex flex-col gap-6">
      {(editable || !!labels.data?.length) && (
        <section aria-labelledby={`labels-${mailbox.id}`} className="flex flex-col gap-1">
          <h2 id={`labels-${mailbox.id}`} className="px-3 text-label text-fg-tertiary">
            Labels
          </h2>
          {labels.data?.map((label) => (
            <OrganizeRow
              key={label.id}
              label={label.name}
              swatch={label.color}
              active={active?.kind === "label" && active.id === label.id}
              editable={editable}
              menuLabel={`More actions for ${label.name}`}
              editLabel="Edit…"
              onOpen={() => onLabel(label.id)}
              onEdit={() => setLabelDialog(label)}
              onDelete={() => {
                setError("")
                setDeleteLabel(label)
              }}
            />
          ))}
          {editable && (
            <button
              type="button"
              onClick={() => setLabelDialog("new")}
              className="flex min-h-10 w-full cursor-pointer items-center gap-3 rounded-lg px-3 text-left text-[15px] font-medium text-fg-tertiary transition-colors duration-160 hover:bg-muted hover:text-foreground max-md:min-h-11"
            >
              <Tag aria-hidden className="size-4 shrink-0" />
              New label
            </button>
          )}
        </section>
      )}
      {(editable || !!searches.data?.length) && (
        <section aria-labelledby={`searches-${mailbox.id}`} className="flex flex-col gap-1">
          <h2 id={`searches-${mailbox.id}`} className="px-3 text-label text-fg-tertiary">
            Saved searches
          </h2>
          {searches.data?.map((search) => (
            <OrganizeRow
              key={search.id}
              label={search.name}
              icon={<Bookmark aria-hidden className="size-4 shrink-0 text-fg-tertiary" />}
              active={active?.kind === "search" && active.id === search.id}
              editable={editable}
              menuLabel={`More actions for ${search.name}`}
              editLabel="Rename…"
              onOpen={() => onSearch(search.id)}
              onEdit={() => {
                setError("")
                setRenameSearch(search)
              }}
              onDelete={() => setDeleteSearch(search)}
            />
          ))}
          {editable && !searches.data?.length && <p className="px-3 text-sm text-fg-tertiary">Save a search from the message list.</p>}
        </section>
      )}
      {failed && (
        <button type="button" className="px-3 py-2 text-left text-sm text-fg-secondary hover:text-foreground" onClick={() => { labels.refetch(); searches.refetch() }}>
          Couldn’t load labels and saved searches. Try again.
        </button>
      )}
      {labelDialog && (
        <LabelDialog
          key={labelDialog === "new" ? "new" : labelDialog.id}
          mailboxId={mailbox.id}
          label={labelDialog === "new" ? undefined : labelDialog}
          open
          onOpenChange={(next) => !next && setLabelDialog(null)}
        />
      )}
      {renameSearch && (
        <SearchDialog
          key={renameSearch.id}
          open
          onOpenChange={(next) => !next && setRenameSearch(null)}
          title="Rename saved search"
          description="The messages stay where they are. This only changes the shortcut."
          submitLabel="Save name"
          initialName={renameSearch.name}
          pending={rename.isPending}
          error={error}
          onSubmit={(name) => rename.mutate({ search: renameSearch, name })}
        />
      )}
      <ConfirmDialog
        open={deleteLabel !== null}
        onOpenChange={(next) => {
          if (!next) {
            setDeleteLabel(null)
            setError("")
          }
        }}
        title={`Delete “${deleteLabel?.name ?? ""}”?`}
        description="Messages lose this label, including in other mail apps. Saved searches that filter by it keep working without the label."
        confirmLabel="Delete label"
        pending={removeLabel.isPending}
        onConfirm={() => deleteLabel && removeLabel.mutate(deleteLabel)}
      >
        {error && <p role="alert" className="text-sm text-danger">{error}</p>}
      </ConfirmDialog>
      <ConfirmDialog
        open={deleteSearch !== null}
        onOpenChange={(next) => !next && setDeleteSearch(null)}
        title={`Delete “${deleteSearch?.name ?? ""}”?`}
        description="The messages stay where they are. This only removes the shortcut."
        confirmLabel="Delete search"
        pending={removeSearch.isPending}
        onConfirm={() => deleteSearch && removeSearch.mutate(deleteSearch)}
      />
    </div>
  )
}

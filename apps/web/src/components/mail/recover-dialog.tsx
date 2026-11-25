"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArchiveRestore, SearchX } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { FormDialog } from "@/components/form-dialog"
import { SearchInput } from "@/components/search-input"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { DialogClose } from "@/components/ui/dialog"
import { api } from "@/lib/api"
import { formatDateTime, formatListDate, plural } from "@/lib/format"
import { folderLabel, senderLabel } from "@/lib/mail"
import type { Mailbox, RecoverableItem, RecoverableList } from "@/lib/types"

const itemKey = (i: RecoverableItem) => `${i.uid}:${i.folder}`

const originLabel = (folder: string) => folderLabel({ name: folder, specialUse: folder === "INBOX" ? "inbox" : "" })

export function RecoverDialog({ mailbox, onOpenChange }: { mailbox: Pick<Mailbox, "id" | "address"> | null; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient()
  const key = ["recoverable", mailbox?.id]
  const list = useQuery({ queryKey: key, queryFn: () => api<RecoverableList>(`/mailboxes/${mailbox?.id}/recoverable`), enabled: !!mailbox })
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [search, setSearch] = useState("")
  const needle = search.trim().toLowerCase()
  const visible = (list.data?.items ?? []).filter((i) =>
    [i.subject, originLabel(i.folder), ...i.from.flatMap((a) => [a.name ?? "", a.address])].some((v) => v.toLowerCase().includes(needle))
  )
  const chosen = list.data?.items.filter((i) => selected.has(itemKey(i))) ?? []
  const allVisible = visible.length > 0 && visible.every((i) => selected.has(itemKey(i)))

  const restore = useMutation({
    mutationFn: async (items: RecoverableItem[]) => {
      let restored = 0
      for (let i = 0; i < items.length; i += 500) {
        const batch = items.slice(i, i + 500).map(({ folder, uid }) => ({ folder, uid }))
        restored += (await api<{ restored: number }>(`/mailboxes/${mailbox?.id}/recoverable/restore`, { method: "POST", body: { items: batch } })).restored
      }
      return restored
    },
    onSuccess: (restored) => toast.success(`Restored ${plural(restored, "message")}`),
    onSettled: () => {
      setSelected(new Set())
      qc.invalidateQueries({ queryKey: key })
      qc.invalidateQueries({ queryKey: ["folders", mailbox?.id] })
      qc.invalidateQueries({ queryKey: ["messages", mailbox?.id] })
    },
  })

  function toggle(keys: string[], on: boolean) {
    setSelected((current) => {
      const next = new Set(current)
      for (const k of keys) {
        if (on) next.add(k)
        else next.delete(k)
      }
      return next
    })
  }

  const days = list.data?.retentionDays

  return (
    <FormDialog
      open={!!mailbox}
      onOpenChange={(open) => {
        onOpenChange(open)
        if (!open) {
          setSelected(new Set())
          setSearch("")
        }
      }}
      title="Recover deleted items"
      description={`Messages permanently deleted from ${mailbox?.address ?? "this mailbox"}${days ? ` in the last ${plural(days, "day")}` : ""}, including those emptied from Trash. Restored messages go back to the folder they were deleted from, or to Inbox if that folder no longer exists.`}
      submitLabel="Restore"
      wide
      onSubmit={(e) => {
        e.preventDefault()
        if (chosen.length) restore.mutate(chosen)
      }}
      footer={
        <>
          <DialogClose asChild>
            <Button type="button" variant="secondary">
              Close
            </Button>
          </DialogClose>
          <Button type="submit" disabled={!chosen.length} loading={restore.isPending}>
            {chosen.length ? `Restore ${plural(chosen.length, "message")}` : "Restore"}
          </Button>
        </>
      }
    >
      <QueryView
        query={list}
        isEmpty={(d) => d.items.length === 0}
        empty={
          <EmptyState
            icon={ArchiveRestore}
            title="Nothing to recover"
            description={`Messages that are permanently deleted stay here for ${days ? plural(days, "day") : "a limited time"} before they are gone for good.`}
          />
        }
      >
        {(data) => (
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
              <label className="flex min-h-10 shrink-0 cursor-pointer items-center gap-3 text-sm text-fg-secondary">
                <input
                  type="checkbox"
                  className="size-4 cursor-pointer accent-ember"
                  checked={allVisible}
                  disabled={!visible.length}
                  onChange={(e) => toggle(visible.map(itemKey), e.target.checked)}
                />
                {needle ? `Select all ${visible.length} matches` : `Select all ${data.items.length}`}
              </label>
              <SearchInput
                aria-label="Filter deleted messages"
                placeholder="Filter by subject, sender or folder"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="flex-1"
              />
            </div>
            {visible.length === 0 ? (
              <EmptyState icon={SearchX} title="No matches" description="No deleted message matches this filter." className="py-8" />
            ) : (
              <ul aria-label="Deleted messages" className="flex max-h-[50dvh] flex-col divide-y divide-border overflow-y-auto rounded-xl border border-border">
                {visible.map((i) => (
                  <li key={itemKey(i)}>
                    <label className="flex cursor-pointer items-start gap-3 px-4 py-3 transition-colors duration-160 hover:bg-muted">
                      <input
                        type="checkbox"
                        className="mt-1 size-4 shrink-0 cursor-pointer accent-ember"
                        checked={selected.has(itemKey(i))}
                        onChange={(e) => toggle([itemKey(i)], e.target.checked)}
                      />
                      <span className="flex min-w-0 flex-1 flex-col gap-1">
                        <span className="flex items-center gap-3">
                          <span className="flex-1 truncate font-semibold">{senderLabel(i.from)}</span>
                          <time dateTime={i.deletedAt} title={`Deleted ${formatDateTime(i.deletedAt)}`} className="shrink-0 text-xs text-fg-tertiary tabular-nums">
                            {formatListDate(i.deletedAt)}
                          </time>
                        </span>
                        <span className="truncate text-sm text-fg-secondary">{i.subject || "(no subject)"}</span>
                        <span className="truncate text-xs text-fg-tertiary">Deleted from {originLabel(i.folder)}</span>
                      </span>
                    </label>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </QueryView>
    </FormDialog>
  )
}

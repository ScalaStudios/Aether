"use client"

import { useMutation, useQuery, useQueryClient, type InfiniteData, type UseInfiniteQueryResult } from "@tanstack/react-query"
import { FolderOpen, ShieldCheck, Trash2, UserCheck } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { IconButton } from "@/components/icon-button"
import { EmptyState, ErrorState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { api } from "@/lib/api"
import { formatDate, formatListDate, plural } from "@/lib/format"
import { quarantineCategories } from "@/lib/quarantine"
import type { AllowedSender, QuarantineItem, QuarantinePage } from "@/lib/types"

function AllowedSendersDialog({ mailboxId, open, onOpenChange, readOnly }: { mailboxId: string; open: boolean; onOpenChange: (open: boolean) => void; readOnly?: boolean }) {
  const qc = useQueryClient()
  const senders = useQuery({
    queryKey: ["allowed-senders", mailboxId],
    queryFn: () => api<AllowedSender[]>(`/mailboxes/${mailboxId}/allowed-senders`),
    enabled: open,
  })
  const remove = useMutation({
    mutationFn: (sender: string) => api(`/mailboxes/${mailboxId}/allowed-senders`, { method: "DELETE", query: { sender } }),
    onSuccess: (_d, sender) => {
      toast.success(`Mail from ${sender} can be held again`)
      qc.invalidateQueries({ queryKey: ["allowed-senders", mailboxId] })
      qc.invalidateQueries({ queryKey: ["sender-lists", "mailbox", mailboxId] })
    },
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Allowed senders</DialogTitle>
          <DialogDescription>
            Mail from these addresses skips the spam quarantine and goes to your inbox. Phishing and malware are still held, and so is mail that fails the sender’s own
            authentication checks.
          </DialogDescription>
        </DialogHeader>
        <QueryView
          query={senders}
          skeleton={<RowsSkeleton rows={2} />}
          empty={<p className="text-sm text-fg-tertiary">No allowed senders yet. Choose Release and allow sender on a held message to add one.</p>}
        >
          {(list) => (
            <ul className="flex max-h-80 flex-col divide-y divide-border overflow-y-auto rounded-xl border border-border">
              {list.map((s) => (
                <li key={s.sender} className="flex items-center gap-3 px-4 py-2">
                  <div className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate font-mono text-sm">{s.sender}</span>
                    <span className="text-xs text-fg-tertiary">Added {formatDate(s.createdAt)}</span>
                  </div>
                  {!readOnly && (
                    <IconButton label={`Stop allowing ${s.sender}`} size="icon-sm" disabled={remove.isPending} onClick={() => remove.mutate(s.sender)}>
                      <Trash2 />
                    </IconButton>
                  )}
                </li>
              ))}
            </ul>
          )}
        </QueryView>
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="secondary">Done</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function QuarantineList({
  className,
  mailboxId,
  query,
  items,
  selectedId,
  readOnly,
  onOpen,
  onShowFolders,
}: {
  className?: string
  mailboxId: string
  query: UseInfiniteQueryResult<InfiniteData<QuarantinePage>>
  items: QuarantineItem[]
  selectedId: string | null
  readOnly?: boolean
  onOpen: (id: string) => void
  onShowFolders: () => void
}) {
  const [allowedOpen, setAllowedOpen] = useState(false)
  const first = query.data?.pages[0]
  const retention = first?.retentionDays ?? 30

  return (
    <section aria-label="Quarantine" className={cn("min-h-0 flex-col border-r border-border", className)}>
      <div className="flex flex-col gap-1 border-b border-border p-4">
        <div className="flex items-center gap-2">
          <IconButton label="Show folders" size="icon-sm" className="lg:hidden" onClick={onShowFolders}>
            <FolderOpen />
          </IconButton>
          <h1 className="flex-1 truncate text-h3">Quarantine</h1>
          {first?.total !== undefined && <span className="text-xs text-fg-tertiary tabular-nums">{plural(first.total, "message")}</span>}
          <IconButton label="Allowed senders" size="icon-sm" onClick={() => setAllowedOpen(true)}>
            <UserCheck />
          </IconButton>
        </div>
        <p className="text-sm text-fg-tertiary">Held mail is deleted {retention} days after it arrives.</p>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {query.isPending ? (
          <RowsSkeleton rows={6} className="p-4" />
        ) : query.isError ? (
          <ErrorState error={query.error} onRetry={() => query.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState
            icon={ShieldCheck}
            title="Nothing is held"
            description={`Mail that looks like spam, phishing or malware waits here for ${retention} days instead of reaching your inbox, so you can get back anything held by mistake.`}
          />
        ) : (
          <ul className="animate-content">
            {items.map((it) => {
              const category = quarantineCategories[it.category]
              return (
                <li key={it.id}>
                  <button
                    type="button"
                    aria-current={it.id === selectedId ? "true" : undefined}
                    onClick={() => onOpen(it.id)}
                    className={cn(
                      "relative flex w-full cursor-pointer flex-col gap-1 border-b border-border px-4 py-3 text-left transition-colors duration-160 hover:bg-muted/60",
                      it.id === selectedId && "bg-muted before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-ember"
                    )}
                  >
                    <span className="flex items-center gap-2">
                      <span className="flex-1 truncate font-semibold">{it.from.name || it.from.address || "Unknown sender"}</span>
                      <time dateTime={it.receivedAt} className="shrink-0 text-xs text-fg-tertiary tabular-nums">
                        {formatListDate(it.receivedAt)}
                      </time>
                    </span>
                    <span className="truncate text-sm text-fg-secondary">{it.subject || "(no subject)"}</span>
                    <span className="flex flex-wrap items-center gap-2 pt-1">
                      <StatusBadge tone={category.tone}>{category.label}</StatusBadge>
                      {it.status === "requested" && <StatusBadge tone="info">Release requested</StatusBadge>}
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
        )}
        {query.hasNextPage && (
          <div className="flex justify-center p-4">
            <Button variant="secondary" size="sm" loading={query.isFetchingNextPage} onClick={() => query.fetchNextPage()}>
              Load older
            </Button>
          </div>
        )}
      </div>
      <AllowedSendersDialog mailboxId={mailboxId} open={allowedOpen} onOpenChange={setAllowedOpen} readOnly={readOnly} />
    </section>
  )
}

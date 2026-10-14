"use client"

import type { UseQueryResult } from "@tanstack/react-query"
import { PenSquare } from "lucide-react"
import { cn } from "cn"

import { ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { folderIcons, folderLabel } from "@/lib/mail"
import type { Folder, Mailbox } from "@/lib/types"

export function FolderPane({
  className,
  mailboxes,
  mailbox,
  onMailboxChange,
  folders,
  folder,
  onFolderChange,
  onCompose,
}: {
  className?: string
  mailboxes: Mailbox[]
  mailbox: Mailbox
  onMailboxChange: (id: string) => void
  folders: UseQueryResult<Folder[]>
  folder: string
  onFolderChange: (name: string) => void
  onCompose: () => void
}) {
  return (
    <aside aria-label="Folders" className={cn("min-h-0 flex-col gap-6 border-r border-border bg-card p-4", className)}>
      {mailboxes.length > 1 ? (
        <Select value={mailbox.id} onValueChange={onMailboxChange}>
          <SelectTrigger aria-label="Mailbox" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {mailboxes.map((m) => (
              <SelectItem key={m.id} value={m.id}>
                {m.address}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <p className="truncate px-2 text-sm text-fg-tertiary">{mailbox.address}</p>
      )}
      <Button onClick={onCompose} className="w-full">
        <PenSquare aria-hidden />
        New message
      </Button>
      <nav aria-label="Mail folders" className="-mx-2 flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2">
        {folders.isPending ? (
          <RowsSkeleton rows={5} />
        ) : folders.isError ? (
          <ErrorState error={folders.error} onRetry={() => folders.refetch()} className="py-6" />
        ) : (
          folders.data.map((f) => {
            const Icon = folderIcons[f.specialUse]
            const active = f.name === folder
            return (
              <button
                key={f.name}
                type="button"
                aria-current={active ? "page" : undefined}
                onClick={() => onFolderChange(f.name)}
                className={cn(
                  "flex min-h-10 w-full cursor-pointer items-center gap-3 rounded-lg px-3 text-left text-[15px] font-medium text-fg-secondary transition-colors duration-160 hover:bg-muted hover:text-foreground max-md:min-h-11",
                  active && "bg-muted text-foreground"
                )}
              >
                <Icon aria-hidden className={cn("size-4 shrink-0 text-fg-tertiary", active && "text-ember")} />
                <span className="flex-1 truncate">{folderLabel(f)}</span>
                {f.unread > 0 && (
                  <span className="text-xs font-semibold tabular-nums text-foreground">
                    {f.unread}
                    <span className="sr-only"> unread</span>
                  </span>
                )}
              </button>
            )
          })
        )}
      </nav>
    </aside>
  )
}

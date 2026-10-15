"use client"

import type { UseQueryResult } from "@tanstack/react-query"
import { CalendarClock, PenSquare, type LucideIcon } from "lucide-react"
import { cn } from "cn"

import { ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useSession } from "@/hooks/use-session"
import { folderIcons, folderLabel } from "@/lib/mail"
import type { Folder, Mailbox } from "@/lib/types"

function FolderButton({ icon: Icon, label, count, countLabel, active, onClick }: { icon: LucideIcon; label: string; count: number; countLabel: string; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      aria-current={active ? "page" : undefined}
      onClick={onClick}
      className={cn(
        "flex min-h-10 w-full cursor-pointer items-center gap-3 rounded-lg px-3 text-left text-[15px] font-medium text-fg-secondary transition-colors duration-160 hover:bg-muted hover:text-foreground max-md:min-h-11",
        active && "bg-muted text-foreground"
      )}
    >
      <Icon aria-hidden className={cn("size-4 shrink-0 text-fg-tertiary", active && "text-ember")} />
      <span className="flex-1 truncate">{label}</span>
      {count > 0 && (
        <span className="text-xs font-semibold tabular-nums text-foreground">
          {count}
          <span className="sr-only"> {countLabel}</span>
        </span>
      )}
    </button>
  )
}

export function FolderPane({
  className,
  mailboxes,
  mailbox,
  onMailboxChange,
  folders,
  folder,
  onFolderChange,
  scheduled = 0,
  scheduledActive = false,
  onScheduled,
  onCompose,
}: {
  className?: string
  mailboxes: Mailbox[]
  mailbox: Mailbox
  onMailboxChange: (id: string) => void
  folders: UseQueryResult<Folder[]>
  folder: string
  onFolderChange: (name: string) => void
  scheduled?: number
  scheduledActive?: boolean
  onScheduled?: () => void
  onCompose?: () => void
}) {
  const userId = useSession().data?.user.id
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
                <span className="truncate">{m.address}</span>
                {userId && m.ownerUserId !== userId && <span className="text-xs text-fg-tertiary">Shared</span>}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <p className="truncate px-2 text-sm text-fg-tertiary">{mailbox.address}</p>
      )}
      {onCompose ? (
        <Button onClick={onCompose} className="w-full">
          <PenSquare aria-hidden />
          New message
        </Button>
      ) : (
        <p className="rounded-lg bg-muted px-3 py-2 text-sm text-fg-secondary">Read only. You can read this mailbox but not send, move, or delete.</p>
      )}
      <nav aria-label="Mail folders" className="-mx-2 flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2">
        {folders.isPending ? (
          <RowsSkeleton rows={5} />
        ) : folders.isError ? (
          <ErrorState error={folders.error} onRetry={() => folders.refetch()} className="py-6" />
        ) : (
          <>
            {folders.data.map((f) => (
              <FolderButton
                key={f.name}
                icon={folderIcons[f.specialUse]}
                label={folderLabel(f)}
                count={f.unread}
                countLabel="unread"
                active={!scheduledActive && f.name === folder}
                onClick={() => onFolderChange(f.name)}
              />
            ))}
            {scheduled > 0 && onScheduled && (
              <FolderButton icon={CalendarClock} label="Scheduled" count={scheduled} countLabel="scheduled" active={scheduledActive} onClick={onScheduled} />
            )}
          </>
        )}
      </nav>
    </aside>
  )
}

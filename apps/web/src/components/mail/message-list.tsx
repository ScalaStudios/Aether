"use client"

import type { InfiniteData, UseInfiniteQueryResult } from "@tanstack/react-query"
import { AlarmClock, Flag, FolderOpen, Keyboard, Paperclip, PenSquare, SearchX } from "lucide-react"
import { useEffect, useRef } from "react"
import { cn } from "cn"

import { IconButton } from "@/components/icon-button"
import { SearchInput } from "@/components/search-input"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { formatListDate, formatWhen } from "@/lib/format"
import { folderIcons, folderLabel, senderLabel } from "@/lib/mail"
import type { Folder, MessagePage, MessageSummary } from "@/lib/types"

export function MessageList({
  className,
  folder,
  search,
  onSearchChange,
  searchRef,
  q,
  query,
  summaries,
  selectedUid,
  onOpen,
  onShowFolders,
  onCompose,
  onHelp,
  wakeTimes,
}: {
  className?: string
  folder: Pick<Folder, "name" | "specialUse">
  search: string
  onSearchChange: (value: string) => void
  searchRef: React.RefObject<HTMLInputElement | null>
  q: string
  query: UseInfiniteQueryResult<InfiniteData<MessagePage>>
  summaries: MessageSummary[]
  selectedUid: number | null
  onOpen: (summary: MessageSummary) => void
  onShowFolders: () => void
  onCompose: () => void
  onHelp: () => void
  wakeTimes?: Map<number, string>
}) {
  const sentinel = useRef<HTMLDivElement>(null)
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query

  useEffect(() => {
    const el = sentinel.current
    if (!el || !hasNextPage) return
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting && !isFetchingNextPage) fetchNextPage()
      },
      { rootMargin: "200px" }
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [hasNextPage, isFetchingNextPage, fetchNextPage])

  useEffect(() => {
    if (selectedUid !== null) document.getElementById(`message-${selectedUid}`)?.scrollIntoView({ block: "nearest" })
  }, [selectedUid])

  const FolderIcon = folderIcons[folder.specialUse]
  const total = query.data?.pages[0]?.total

  return (
    <section aria-label="Messages" className={cn("min-h-0 flex-col border-r border-border", className)}>
      <div className="flex flex-col gap-3 border-b border-border p-4">
        <div className="flex items-center gap-2">
          <IconButton label="Show folders" size="icon-sm" className="lg:hidden" onClick={onShowFolders}>
            <FolderOpen />
          </IconButton>
          <h1 className="flex-1 truncate text-h3">{folderLabel(folder)}</h1>
          {total !== undefined && (
            <span className="text-xs text-fg-tertiary tabular-nums">{total} {total === 1 ? "message" : "messages"}</span>
          )}
          <IconButton label="New message" size="icon-sm" className="lg:hidden" onClick={onCompose}>
            <PenSquare />
          </IconButton>
          <IconButton label="Keyboard shortcuts" size="icon-sm" className="max-md:hidden" onClick={onHelp}>
            <Keyboard />
          </IconButton>
        </div>
        <SearchInput ref={searchRef} aria-label="Search mail" placeholder="Search this folder" value={search} onChange={(e) => onSearchChange(e.target.value)} />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {query.isPending ? (
          <RowsSkeleton rows={8} className="p-4" />
        ) : query.isError ? (
          <ErrorState error={query.error} onRetry={() => query.refetch()} />
        ) : summaries.length === 0 ? (
          q ? (
            <EmptyState icon={SearchX} title="No matches" description={`No messages in ${folderLabel(folder)} match “${q}”.`} />
          ) : (
            <EmptyState icon={FolderIcon} title={`${folderLabel(folder)} is empty`} description="New messages appear here automatically." />
          )
        ) : (
          <ul className="animate-content">
            {summaries.map((m) => (
              <li key={m.uid}>
                <button
                  id={`message-${m.uid}`}
                  type="button"
                  aria-current={m.uid === selectedUid ? "true" : undefined}
                  onClick={() => onOpen(m)}
                  className={cn(
                    "relative flex w-full cursor-pointer flex-col gap-1 border-b border-border px-4 py-3 text-left transition-colors duration-160 hover:bg-muted/60",
                    m.uid === selectedUid && "bg-muted before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-ember"
                  )}
                >
                  <span className="flex items-center gap-2">
                    {!m.seen && <span aria-hidden className="size-2 shrink-0 rounded-full bg-ember" />}
                    <span className={cn("flex-1 truncate", m.seen ? "text-fg-secondary" : "font-semibold text-foreground")}>
                      {!m.seen && <span className="sr-only">Unread, </span>}
                      {folder.specialUse === "sent" || folder.specialUse === "drafts" ? senderLabel(m.to, "No recipients") : senderLabel(m.from)}
                    </span>
                    <time dateTime={m.date} className="shrink-0 text-xs text-fg-tertiary tabular-nums">
                      {formatListDate(m.date)}
                    </time>
                  </span>
                  <span className="flex items-center gap-2">
                    <span className={cn("flex-1 truncate text-sm", m.seen ? "text-fg-tertiary" : "font-semibold text-fg-secondary")}>
                      {m.subject || "(no subject)"}
                    </span>
                    {(m.threadUids?.length ?? 0) > 1 && (
                      <span aria-label={`${m.threadUids?.length} messages in this conversation`} className="shrink-0 rounded-full bg-muted px-2 text-xs text-fg-secondary">
                        {m.threadUids?.length}
                      </span>
                    )}
                    {m.hasAttachments && <Paperclip aria-label="Has attachments" className="size-3.5 shrink-0 text-fg-tertiary" />}
                    {m.flagged && <Flag aria-label="Flagged" className="size-3.5 shrink-0 fill-current text-ember" />}
                  </span>
                  {wakeTimes?.has(m.uid) && (
                    <span className="flex items-center gap-1 text-xs text-fg-tertiary">
                      <AlarmClock aria-hidden className="size-3.5" />
                      Wakes {formatWhen(new Date(wakeTimes.get(m.uid) ?? ""))}
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        )}
        <div ref={sentinel} />
        {isFetchingNextPage && <RowsSkeleton rows={2} className="p-4" />}
      </div>
    </section>
  )
}

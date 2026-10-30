"use client"

import type { InfiniteData, UseInfiniteQueryResult } from "@tanstack/react-query"
import { AlarmClock, ArrowDownUp, Flag, FolderOpen, FunnelX, Keyboard, Paperclip, PenSquare, SearchX } from "lucide-react"
import { useEffect, useRef } from "react"
import { cn } from "cn"

import { IconButton } from "@/components/icon-button"
import { SearchInput } from "@/components/search-input"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { formatBytes, formatListDate, formatWhen } from "@/lib/format"
import { dragTypes, folderIcons, folderLabel, senderLabel, type ListView, type MessageSort, type QuickFilter } from "@/lib/mail"
import type { Folder, MessagePage, MessageSummary } from "@/lib/types"

const quickFilters: { value: QuickFilter; label: string }[] = [
  { value: "unread", label: "Unread" },
  { value: "flagged", label: "Flagged" },
  { value: "hasAttachments", label: "Attachments" },
]

const alphabetical = [
  { value: "asc", label: "A to Z" },
  { value: "desc", label: "Z to A" },
] as const

const orders: Record<MessageSort, readonly { value: ListView["order"]; label: string }[]> = {
  date: [
    { value: "desc", label: "Newest first" },
    { value: "asc", label: "Oldest first" },
  ],
  from: alphabetical,
  to: alphabetical,
  subject: alphabetical,
  size: [
    { value: "desc", label: "Largest first" },
    { value: "asc", label: "Smallest first" },
  ],
}

function SortMenu({ view, onViewChange, outgoing }: { view: ListView; onViewChange: (view: ListView) => void; outgoing: boolean }) {
  const keys: { value: MessageSort; label: string }[] = [
    { value: "date", label: "Date" },
    outgoing ? { value: "to", label: "Recipient" } : { value: "from", label: "Sender" },
    { value: "subject", label: "Subject" },
    { value: "size", label: "Size" },
  ]
  const order = orders[view.sort].find((o) => o.value === view.order)?.label
  const current = view.sort === "date" ? order : `${keys.find((k) => k.value === view.sort)?.label}, ${order?.toLowerCase()}`
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button variant="quiet" size="icon-sm" aria-label={`Sort messages: ${current}`} className={cn((view.sort !== "date" || view.order !== "desc") && "text-ember")}>
              <ArrowDownUp />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>Sort: {current}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">Sort by</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={view.sort} onValueChange={(v) => onViewChange({ ...view, sort: v as MessageSort, order: orders[v as MessageSort][0].value })}>
          {keys.map((k) => (
            <DropdownMenuRadioItem key={k.value} value={k.value}>
              {k.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">Order</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={view.order} onValueChange={(v) => onViewChange({ ...view, order: v as ListView["order"] })}>
          {orders[view.sort].map((o) => (
            <DropdownMenuRadioItem key={o.value} value={o.value}>
              {o.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function filteredDescription(folder: string, filters: QuickFilter[]) {
  const adjectives = filters.filter((f) => f !== "hasAttachments").join(", ")
  return `${folder} has no ${adjectives ? `${adjectives} ` : ""}messages${filters.includes("hasAttachments") ? " with attachments" : ""}.`
}

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
  view,
  onViewChange,
  draggable,
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
  onCompose?: () => void
  onHelp: () => void
  wakeTimes?: Map<number, string>
  view: ListView
  onViewChange: (view: ListView) => void
  draggable?: boolean
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
  const outgoing = folder.specialUse === "sent" || folder.specialUse === "drafts"

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
          {onCompose && (
            <IconButton label="New message" size="icon-sm" className="lg:hidden" onClick={onCompose}>
              <PenSquare />
            </IconButton>
          )}
          <SortMenu view={view} onViewChange={onViewChange} outgoing={outgoing} />
          <IconButton label="Keyboard shortcuts" size="icon-sm" className="max-md:hidden" onClick={onHelp}>
            <Keyboard />
          </IconButton>
        </div>
        <SearchInput ref={searchRef} aria-label="Search mail" placeholder="Search this folder" value={search} onChange={(e) => onSearchChange(e.target.value)} />
        <div className="flex">
          <SegmentedControl
            label="Show only"
            options={quickFilters}
            value={view.filters}
            onChange={(f) => onViewChange({ ...view, filters: view.filters.includes(f) ? view.filters.filter((x) => x !== f) : [...view.filters, f] })}
          />
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {query.isPending ? (
          <RowsSkeleton rows={8} className="p-4" />
        ) : query.isError ? (
          <ErrorState error={query.error} onRetry={() => query.refetch()} />
        ) : summaries.length === 0 ? (
          q ? (
            <EmptyState icon={SearchX} title="No matches" description={`No messages in ${folderLabel(folder)} match “${q}”.`} />
          ) : view.filters.length > 0 ? (
            <EmptyState
              icon={FunnelX}
              title="No matching messages"
              description={filteredDescription(folderLabel(folder), view.filters)}
              action={
                <Button variant="secondary" onClick={() => onViewChange({ ...view, filters: [] })}>
                  Clear filters
                </Button>
              }
            />
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
                  draggable={draggable}
                  onDragStart={(e) => e.dataTransfer.setData(dragTypes.message, String(m.uid))}
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
                      {outgoing ? senderLabel(m.to, "No recipients") : senderLabel(m.from)}
                    </span>
                    {view.sort === "size" ? (
                      <span className="shrink-0 text-xs text-fg-tertiary tabular-nums">{formatBytes(m.size)}</span>
                    ) : (
                      <time dateTime={m.date} className="shrink-0 text-xs text-fg-tertiary tabular-nums">
                        {formatListDate(m.date)}
                      </time>
                    )}
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

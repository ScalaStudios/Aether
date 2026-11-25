"use client"

import type { InfiniteData, UseInfiniteQueryResult } from "@tanstack/react-query"
import { AlarmClock, ArrowDownUp, Bookmark, Flag, FolderOpen, FunnelX, Info, Keyboard, Paperclip, PenSquare, SearchX, Tag } from "lucide-react"
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
import { LabelChips, LabelDot } from "@/components/mail/label-controls"
import { formatBytes, formatListDate, formatWhen } from "@/lib/format"
import { useI18n } from "@/lib/i18n"
import { beginMessageDrag, dragTypes, endMessageDrag, folderIcons, folderLabel, senderLabel, type ListView, type MessageSort, type QuickFilter } from "@/lib/mail"
import type { Folder, Label, MessagePage, MessageSummary } from "@/lib/types"

const alphabetical = [
  { value: "asc", labelKey: "mail.list.sort.aToZ" },
  { value: "desc", labelKey: "mail.list.sort.zToA" },
] as const

const orders: Record<MessageSort, readonly { value: ListView["order"]; labelKey: string }[]> = {
  date: [
    { value: "desc", labelKey: "mail.list.sort.newestFirst" },
    { value: "asc", labelKey: "mail.list.sort.oldestFirst" },
  ],
  from: alphabetical,
  to: alphabetical,
  subject: alphabetical,
  size: [
    { value: "desc", labelKey: "mail.list.sort.largestFirst" },
    { value: "asc", labelKey: "mail.list.sort.smallestFirst" },
  ],
}

function SortMenu({ view, onViewChange, outgoing }: { view: ListView; onViewChange: (view: ListView) => void; outgoing: boolean }) {
  const { t } = useI18n()
  const keys: { value: MessageSort; label: string }[] = [
    { value: "date", label: t("mail.list.sort.date") },
    outgoing ? { value: "to", label: t("mail.list.sort.recipient") } : { value: "from", label: t("mail.list.sort.sender") },
    { value: "subject", label: t("mail.list.sort.subject") },
    { value: "size", label: t("mail.list.sort.size") },
  ]
  const orderKey = orders[view.sort].find((o) => o.value === view.order)?.labelKey
  const order = orderKey ? t(orderKey) : ""
  const current = view.sort === "date" ? order : `${keys.find((k) => k.value === view.sort)?.label}, ${order.toLowerCase()}`
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button variant="quiet" size="icon-sm" aria-label={t("mail.list.sort.ariaLabel", { current })} className={cn((view.sort !== "date" || view.order !== "desc") && "text-ember")}>
              <ArrowDownUp />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>{t("mail.list.sort.tooltip", { current })}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">{t("mail.list.sort.sortBy")}</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={view.sort} onValueChange={(v) => onViewChange({ ...view, sort: v as MessageSort, order: orders[v as MessageSort][0].value })}>
          {keys.map((k) => (
            <DropdownMenuRadioItem key={k.value} value={k.value}>
              {k.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">{t("mail.list.sort.order")}</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={view.order} onValueChange={(v) => onViewChange({ ...view, order: v as ListView["order"] })}>
          {orders[view.sort].map((o) => (
            <DropdownMenuRadioItem key={o.value} value={o.value}>
              {t(o.labelKey)}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function filteredDescription(folder: string, filters: QuickFilter[], label?: string) {
  const adjectives = filters.filter((f) => f !== "hasAttachments").join(", ")
  return `${folder} has no ${adjectives ? `${adjectives} ` : ""}messages${filters.includes("hasAttachments") ? " with attachments" : ""}${label ? ` labeled ${label}` : ""}.`
}

export function MessageList({
  className,
  folder,
  notice,
  search,
  onSearchChange,
  searchRef,
  q,
  query,
  summaries,
  selected,
  onOpen,
  onShowFolders,
  onCompose,
  onHelp,
  wakeTimes,
  view,
  onViewChange,
  draggable,
  live,
  labels,
  labelFilter,
  heading,
  swatch,
  searchPlaceholder = "Search this folder",
  showFolder,
  folderTitle,
  saveSearch,
  emptyTitle,
  emptyDescription,
}: {
  className?: string
  folder: Pick<Folder, "name" | "specialUse">
  notice?: string
  search: string
  onSearchChange: (value: string) => void
  searchRef: React.RefObject<HTMLInputElement | null>
  q: string
  query: UseInfiniteQueryResult<InfiniteData<MessagePage>>
  summaries: MessageSummary[]
  selected: { uid: number; folder: string } | null
  onOpen: (summary: MessageSummary) => void
  onShowFolders: () => void
  onCompose?: () => void
  onHelp: () => void
  wakeTimes?: Map<number, string>
  view: ListView
  onViewChange: (view: ListView) => void
  draggable?: boolean
  live?: boolean
  labels?: Label[]
  labelFilter?: boolean
  heading?: string
  swatch?: string
  searchPlaceholder?: string
  showFolder?: boolean
  folderTitle?: (name: string) => string
  saveSearch?: { label: string; onClick: () => void }
  emptyTitle?: string
  emptyDescription?: string
}) {
  const { t, tp } = useI18n()
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
    if (selected) document.getElementById(`message-${selected.folder}-${selected.uid}`)?.scrollIntoView({ block: "nearest" })
  }, [selected])

  const FolderIcon = folderIcons[folder.specialUse]
  const total = query.data?.pages[0]?.total
  const outgoing = folder.specialUse === "sent" || folder.specialUse === "drafts"
  const title = heading ?? folderLabel(folder)
  const activeLabel = labels?.find((label) => label.id === view.labelId)
  const narrowed = view.filters.length > 0 || !!view.labelId
  const quickFilters: { value: QuickFilter; label: string }[] = [
    { value: "unread", label: t("mail.list.filters.unread") },
    { value: "flagged", label: t("mail.list.filters.flagged") },
    { value: "hasAttachments", label: t("mail.list.filters.hasAttachments") },
  ]

  return (
    <section aria-label={t("mail.list.sectionLabel")} className={cn("min-h-0 flex-col border-r border-border", className)}>
      <div className="flex flex-col gap-3 border-b border-border p-4">
        <div className="flex items-center gap-2">
          <IconButton label={t("mail.list.showFolders")} size="icon-sm" className="lg:hidden" onClick={onShowFolders}>
            <FolderOpen />
          </IconButton>
          {swatch && <LabelDot color={swatch} />}
          <h1 className="flex-1 truncate text-h3">{title}</h1>
          {total !== undefined && (
            <span className="text-xs text-fg-tertiary tabular-nums">{tp("mail.messageCount", total)}</span>
          )}
          <span role="status" className="flex items-center gap-1.5 text-xs text-fg-tertiary">
            <span aria-hidden className={cn("size-1.5 rounded-full", live ? "bg-success" : "bg-fg-tertiary")} />
            <span className={cn(!live && "max-sm:sr-only")}>{live ? "Live" : "Every 30s"}</span>
          </span>
          {onCompose && (
            <IconButton label={t("mail.folders.newMessage")} size="icon-sm" className="lg:hidden" onClick={onCompose}>
              <PenSquare />
            </IconButton>
          )}
          {labelFilter && !!labels?.length && (
            <DropdownMenu>
              <Tooltip>
                <TooltipTrigger asChild>
                  <DropdownMenuTrigger asChild>
                    <Button variant="quiet" size="icon-sm" aria-label={activeLabel ? `Label: ${activeLabel.name}` : "Filter by label"} className={cn(activeLabel && "text-ember")}>
                      {activeLabel ? <LabelDot color={activeLabel.color} /> : <Tag />}
                    </Button>
                  </DropdownMenuTrigger>
                </TooltipTrigger>
                <TooltipContent>{activeLabel ? activeLabel.name : "Label"}</TooltipContent>
              </Tooltip>
              <DropdownMenuContent align="end" className="w-56">
                <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">Label</DropdownMenuLabel>
                <DropdownMenuRadioGroup value={view.labelId ?? "any"} onValueChange={(value) => onViewChange({ ...view, labelId: value === "any" ? undefined : value })}>
                  <DropdownMenuRadioItem value="any">Any label</DropdownMenuRadioItem>
                  {labels.map((label) => (
                    <DropdownMenuRadioItem key={label.id} value={label.id}>
                      <LabelDot color={label.color} />
                      <span className="truncate">{label.name}</span>
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          <SortMenu view={view} onViewChange={onViewChange} outgoing={outgoing} />
          <IconButton label={t("mail.list.keyboardShortcuts")} size="icon-sm" className="max-md:hidden" onClick={onHelp}>
            <Keyboard />
          </IconButton>
        </div>
        <SearchInput ref={searchRef} aria-label="Search mail" placeholder={searchPlaceholder} value={search} onChange={(e) => onSearchChange(e.target.value)} />
        <div className="flex">
          <SegmentedControl
            label={t("mail.list.showOnly")}
            options={quickFilters}
            value={view.filters}
            onChange={(f) => onViewChange({ ...view, filters: view.filters.includes(f) ? view.filters.filter((x) => x !== f) : [...view.filters, f] })}
          />
        </div>
        {notice && (
          <p className="flex gap-2 text-sm text-fg-tertiary">
            <Info aria-hidden className="size-4 shrink-0 translate-y-0.5" />
            {notice}
          </p>
        )}
        {saveSearch && (
          <Button type="button" variant="secondary" size="sm" className="self-start" onClick={saveSearch.onClick}>
            <Bookmark aria-hidden />
            {saveSearch.label}
          </Button>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {query.isPending ? (
          <RowsSkeleton rows={8} className="p-4" />
        ) : query.isError ? (
          <ErrorState error={query.error} onRetry={() => query.refetch()} />
        ) : summaries.length === 0 ? (
          q ? (
            <EmptyState icon={SearchX} title="No matches" description={emptyDescription ?? `No messages in ${title} match “${q}”.`} />
          ) : narrowed ? (
            <EmptyState
              icon={FunnelX}
              title={t("mail.list.noFilteredTitle")}
              description={filteredDescription(title, view.filters, activeLabel?.name)}
              action={
                <Button variant="secondary" onClick={() => onViewChange({ ...view, filters: [], labelId: undefined })}>
                  {t("mail.list.clearFilters")}
                </Button>
              }
            />
          ) : (
            <EmptyState icon={swatch ? Tag : FolderIcon} title={emptyTitle ?? `${title} is empty`} description={emptyDescription ?? "New messages appear here automatically."} />
          )
        ) : (
          <ul className="animate-content">
            {summaries.map((m) => {
              const messageFolder = m.folder || folder.name
              const current = selected?.uid === m.uid && selected.folder === messageFolder
              return (
              <li key={`${messageFolder}-${m.uid}`}>
                <button
                  id={`message-${messageFolder}-${m.uid}`}
                  type="button"
                  aria-current={current ? "true" : undefined}
                  draggable={draggable}
                  onDragStart={(e) => {
                    e.dataTransfer.setData(dragTypes.message, String(m.uid))
                    e.dataTransfer.setData(dragTypes.messageFolder, messageFolder)
                    beginMessageDrag(m.uid, messageFolder)
                  }}
                  onDragEnd={() => endMessageDrag()}
                  onClick={() => onOpen(m)}
                  className={cn(
                    "relative flex w-full cursor-pointer flex-col gap-1 border-b border-border px-4 py-3 text-left transition-colors duration-160 hover:bg-muted/60",
                    current && "bg-muted before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-ember"
                  )}
                >
                  <span className="flex items-center gap-2">
                    {!m.seen && <span aria-hidden className="size-2 shrink-0 rounded-full bg-ember" />}
                    <span className={cn("flex-1 truncate", m.seen ? "text-fg-secondary" : "font-semibold text-foreground")}>
                      {!m.seen && <span className="sr-only">{t("mail.list.unreadPrefix")}</span>}
                      {outgoing ? senderLabel(m.to, t("mail.noRecipients")) : senderLabel(m.from)}
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
                      {m.subject || t("mail.noSubject")}
                    </span>
                    {(m.threadUids?.length ?? 0) > 1 && (
                      <span aria-label={t("mail.conversationCount", { count: m.threadUids?.length ?? 0 })} className="shrink-0 rounded-full bg-muted px-2 text-xs text-fg-secondary">
                        {m.threadUids?.length}
                      </span>
                    )}
                    {m.hasAttachments && <Paperclip aria-label={t("mail.list.hasAttachments")} className="size-3.5 shrink-0 text-fg-tertiary" />}
                    {m.flagged && <Flag aria-label={t("mail.list.flagged")} className="size-3.5 shrink-0 fill-current text-ember" />}
                  </span>
                  {(showFolder || !!m.keywords?.length || !!m.flagColor) && (
                    <span className="flex items-center gap-2">
                      {showFolder && <span className="shrink-0 text-xs text-fg-tertiary">{folderTitle?.(messageFolder) ?? messageFolder}</span>}
                      <LabelChips labels={labels ?? []} keywords={m.keywords} flagColor={m.flagColor} />
                    </span>
                  )}
                  {wakeTimes?.has(m.uid) && (
                    <span className="flex items-center gap-1 text-xs text-fg-tertiary">
                      <AlarmClock aria-hidden className="size-3.5" />
                      {t("mail.list.wakes", { when: formatWhen(new Date(wakeTimes.get(m.uid) ?? "")) })}
                    </span>
                  )}
                </button>
              </li>
              )
            })}
          </ul>
        )}
        <div ref={sentinel} />
        {isFetchingNextPage && <RowsSkeleton rows={2} className="p-4" />}
      </div>
    </section>
  )
}

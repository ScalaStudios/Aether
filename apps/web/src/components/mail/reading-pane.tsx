"use client"

import { useQueries, type UseQueryResult } from "@tanstack/react-query"
import { AlarmClock, AlarmClockOff, ArrowLeft, ChevronDown, Download, Flag, FolderInput, Forward, Mail, MailOpen, Reply, ReplyAll, Trash2 } from "lucide-react"
import { cn } from "cn"
import { useState } from "react"

import { IconButton } from "@/components/icon-button"
import { InvitationCard } from "@/components/mail/invitation-card"
import { LaterMenu } from "@/components/mail/later-menu"
import { EmptyState, ErrorState } from "@/components/states"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { Button } from "@/components/ui/button"
import { api, apiUrl } from "@/lib/api"
import { formatAddress, formatBytes, formatDateTime } from "@/lib/format"
import { folderIcons, folderLabel, messageDocument, type ComposeMode } from "@/lib/mail"
import type { Address, Folder, Message } from "@/lib/types"

function AddressLine({ label, addresses }: { label: string; addresses: Address[] }) {
  if (!addresses.length) return null
  return (
    <div className="flex gap-2 text-sm">
      <dt className="w-8 shrink-0 text-fg-tertiary">{label}</dt>
      <dd className="min-w-0 break-words text-fg-secondary">{addresses.map(formatAddress).join(", ")}</dd>
    </div>
  )
}

function MessageBody({ message: m, mailboxId, folder }: { message: Message; mailboxId: string; folder: string }) {
  return (
    <>
            {m.attachments.length > 0 && (
        <ul aria-label="Attachments" className="flex flex-wrap gap-2">
          {m.attachments.map((a) => (
            <li key={a.part}>
              <a
                href={apiUrl(`/mailboxes/${mailboxId}/messages/${m.uid}/attachments/${a.part}`, { folder })}
                download={a.filename}
                className="flex min-h-10 max-w-[280px] items-center gap-2 rounded-lg border border-strong px-3 py-2 text-sm transition-colors duration-160 hover:bg-muted"
              >
                <Download aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
                <span className="truncate">{a.filename || "Attachment"}</span>
                <span className="shrink-0 text-xs text-fg-tertiary">{formatBytes(a.size)}</span>
              </a>
            </li>
          ))}
        </ul>
      )}
      {m.html ? (
        <iframe
          title="Message body"
          sandbox="allow-popups allow-popups-to-escape-sandbox"
          srcDoc={messageDocument(m.html)}
          className="min-h-[400px] w-full flex-1 rounded-xl border border-border bg-white"
        />
      ) : (
        <pre className="font-sans text-base whitespace-pre-wrap break-words">{m.text}</pre>
      )}
    </>
  )
}

function messageQuery(mailboxId: string, folder: string, uid: number) {
  return {
    queryKey: ["message", mailboxId, folder, uid],
    queryFn: () => api<Message>(`/mailboxes/${mailboxId}/messages/${uid}`, { query: { folder } }),
  }
}

function Conversation({ message, earlier, mailboxId, folder }: { message: Message; earlier: number[]; mailboxId: string; folder: string }) {
  const items = [...earlier.map((uid) => ({ folder, uid })), ...(message.related ?? [])]
  const queries = useQueries({ queries: items.map((i) => messageQuery(mailboxId, i.folder, i.uid)) })
  if (items.length === 0) return null
  const order = items.map((item, i) => ({ item, query: queries[i] }))
  order.sort((a, b) => (b.query.data ? Date.parse(b.query.data.date) : 0) - (a.query.data ? Date.parse(a.query.data.date) : 0))
  return (
    <section aria-label="More in this conversation" className="flex flex-col gap-2 border-t border-border pt-4">
      <h3 className="text-label text-fg-tertiary">More in this conversation</h3>
      {order.map(({ item, query }) => (
        <EarlierMessage key={`${item.folder}-${item.uid}`} query={query} mailboxId={mailboxId} folder={item.folder} showFolder={item.folder !== folder} />
      ))}
    </section>
  )
}

function EarlierMessage({ query, mailboxId, folder, showFolder }: { query: UseQueryResult<Message>; mailboxId: string; folder: string; showFolder: boolean }) {
  const [open, setOpen] = useState(false)
  const m = query.data
  return (
    <div className="flex flex-col gap-4 rounded-xl border border-border">
      <button
        type="button"
        aria-expanded={open}
        disabled={!m}
        onClick={() => setOpen(!open)}
        className="flex min-h-12 items-center gap-3 rounded-xl px-4 py-2 text-left transition-colors duration-160 hover:bg-muted"
      >
        {m ? (
          <>
            <span className="min-w-0 flex-1 truncate font-semibold">{m.from.map(formatAddress).join(", ") || "Unknown sender"}</span>
            {showFolder && <span className="shrink-0 text-sm text-fg-tertiary">{folder === "INBOX" ? "Inbox" : folder}</span>}
            <time dateTime={m.date} className="shrink-0 text-sm text-fg-tertiary">
              {formatDateTime(m.date)}
            </time>
            <ChevronDown aria-hidden className={cn("size-4 shrink-0 text-fg-tertiary transition-transform duration-160", open && "rotate-180")} />
          </>
        ) : query.isError ? (
          <span className="text-sm text-fg-tertiary">This message could not be loaded.</span>
        ) : (
          <Skeleton className="h-4 w-1/2" />
        )}
      </button>
      {open && m && (
        <div className="flex flex-col gap-4 px-4 pb-4">
          <MessageBody message={m} mailboxId={mailboxId} folder={folder} />
        </div>
      )}
    </div>
  )
}

export function ReadingPane({
  className,
  mailboxId,
  folder,
  folders,
  query,
  selected,
  earlier,
  onBack,
  onReply,
  onFlags,
  onMove,
  onDelete,
  onSnooze,
  onUnsnooze,
  deleting,
  readOnly,
}: {
  className?: string
  mailboxId: string
  folder: string
  folders: Folder[]
  query: UseQueryResult<Message>
  selected: boolean
  earlier: number[]
  onBack: () => void
  onReply: (mode: ComposeMode) => void
  onFlags: (flags: { seen?: boolean; flagged?: boolean }) => void
  onMove: (to: string) => void
  onDelete: () => void
  onSnooze: (until: Date) => void
  onUnsnooze?: () => void
  deleting: boolean
  readOnly?: boolean
}) {
  if (!selected) {
    return (
      <section aria-label="Reading pane" className={cn("items-center justify-center", className)}>
        <EmptyState icon={Mail} title="No message selected" description="Choose a message from the list to read it here. Press ? to see keyboard shortcuts." />
      </section>
    )
  }

  const m = query.data

  return (
    <section aria-label="Reading pane" className={cn("min-h-0 flex-col", className)}>
      <div className="flex shrink-0 items-center gap-1 overflow-x-auto border-b border-border px-4 py-2">
        <IconButton label="Back to messages" className="md:hidden" onClick={onBack}>
          <ArrowLeft />
        </IconButton>
        {!readOnly && (
          <>
            <IconButton label="Reply" disabled={!m} onClick={() => onReply("reply")}>
              <Reply />
            </IconButton>
            <IconButton label="Reply all" disabled={!m} onClick={() => onReply("replyAll")}>
              <ReplyAll />
            </IconButton>
            <IconButton label="Forward" disabled={!m} onClick={() => onReply("forward")}>
              <Forward />
            </IconButton>
            <span className="mx-1 h-6 w-px bg-border" />
            <IconButton label={m?.seen ? "Mark as unread" : "Mark as read"} disabled={!m} onClick={() => m && onFlags({ seen: !m.seen })}>
              {m?.seen ? <Mail /> : <MailOpen />}
            </IconButton>
            <IconButton label={m?.flagged ? "Remove flag" : "Flag"} aria-pressed={m?.flagged ?? false} disabled={!m} onClick={() => m && onFlags({ flagged: !m.flagged })}>
              <Flag className={cn(m?.flagged && "fill-current text-ember")} />
            </IconButton>
            <DropdownMenu>
              <Tooltip>
                <TooltipTrigger asChild>
                  <DropdownMenuTrigger asChild>
                    <Button variant="quiet" size="icon" aria-label="Move to folder" disabled={!m}>
                      <FolderInput />
                    </Button>
                  </DropdownMenuTrigger>
                </TooltipTrigger>
                <TooltipContent>Move to folder</TooltipContent>
              </Tooltip>
              <DropdownMenuContent align="start" className="w-56">
                <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">Move to</DropdownMenuLabel>
                {folders
                  .filter((f) => f.name !== folder)
                  .map((f) => {
                    const Icon = folderIcons[f.specialUse]
                    return (
                      <DropdownMenuItem key={f.name} onSelect={() => onMove(f.name)}>
                        <Icon aria-hidden className="text-fg-tertiary" />
                        {folderLabel(f)}
                      </DropdownMenuItem>
                    )
                  })}
              </DropdownMenuContent>
            </DropdownMenu>
            {onUnsnooze ? (
              <IconButton label="Unsnooze" disabled={!m || deleting} onClick={onUnsnooze}>
                <AlarmClockOff />
              </IconButton>
            ) : (
              <LaterMenu label="Snooze" submitLabel="Snooze" laterToday disabled={!m || deleting} onPick={onSnooze}>
                <Button variant="quiet" size="icon" aria-label="Snooze">
                  <AlarmClock />
                </Button>
              </LaterMenu>
            )}
            <IconButton label="Delete" disabled={!m || deleting} onClick={onDelete}>
              <Trash2 />
            </IconButton>
          </>
        )}
      </div>
      {query.isPending ? (
        <div aria-busy aria-label="Loading message" className="flex flex-col gap-4 p-6">
          <Skeleton className="h-7 w-2/3" />
          <Skeleton className="h-4 w-1/2" />
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="mt-4 h-64 w-full" />
        </div>
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => query.refetch()} />
      ) : (
        m && (
          <article key={m.uid} className="animate-content flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-6">
            <header className="flex flex-col gap-3">
              <h2 className="text-h3 break-words">{m.subject || "(no subject)"}</h2>
              <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <p className="font-semibold break-all">{m.from.map(formatAddress).join(", ") || "Unknown sender"}</p>
                <time dateTime={m.date} className="text-sm text-fg-tertiary">
                  {formatDateTime(m.date)}
                </time>
              </div>
              <dl className="flex flex-col gap-1">
                <AddressLine label="To" addresses={m.to} />
                <AddressLine label="Cc" addresses={m.cc} />
              </dl>
            </header>
            {m.invitation && !readOnly && <InvitationCard key={`${m.uid}-${m.invitation.method}`} message={m} mailboxId={mailboxId} folder={folder} />}
            <MessageBody message={m} mailboxId={mailboxId} folder={folder} />
            <Conversation message={m} earlier={earlier} mailboxId={mailboxId} folder={folder} />
          </article>
        )
      )}
    </section>
  )
}

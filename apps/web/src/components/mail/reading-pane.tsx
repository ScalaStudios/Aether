"use client"

import type { UseQueryResult } from "@tanstack/react-query"
import { ArrowLeft, Download, Flag, FolderInput, Forward, Mail, MailOpen, Reply, ReplyAll, Trash2 } from "lucide-react"
import { cn } from "cn"

import { IconButton } from "@/components/icon-button"
import { EmptyState, ErrorState } from "@/components/states"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { Button } from "@/components/ui/button"
import { apiUrl } from "@/lib/api"
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

export function ReadingPane({
  className,
  mailboxId,
  folder,
  folders,
  query,
  selected,
  onBack,
  onReply,
  onFlags,
  onMove,
  onDelete,
  deleting,
}: {
  className?: string
  mailboxId: string
  folder: string
  folders: Folder[]
  query: UseQueryResult<Message>
  selected: boolean
  onBack: () => void
  onReply: (mode: ComposeMode) => void
  onFlags: (flags: { seen?: boolean; flagged?: boolean }) => void
  onMove: (to: string) => void
  onDelete: () => void
  deleting: boolean
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
        <IconButton label="Delete" disabled={!m || deleting} onClick={onDelete}>
          <Trash2 />
        </IconButton>
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
          </article>
        )
      )}
    </section>
  )
}

"use client"

import { FolderOpen } from "lucide-react"
import { cn } from "cn"

import { IconButton } from "@/components/icon-button"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { formatWhen } from "@/lib/format"
import type { ScheduledMessage } from "@/lib/types"

export function ScheduledList({
  className,
  messages,
  cancelling,
  onCancel,
  onShowFolders,
}: {
  className?: string
  messages: ScheduledMessage[]
  cancelling?: string
  onCancel: (id: string) => void
  onShowFolders: () => void
}) {
  return (
    <section aria-label="Scheduled messages" className={cn("min-h-0 flex-col border-r border-border", className)}>
      <div className="flex items-center gap-2 border-b border-border p-4">
        <IconButton label="Show folders" size="icon-sm" className="lg:hidden" onClick={onShowFolders}>
          <FolderOpen />
        </IconButton>
        <h1 className="flex-1 truncate text-h3">Scheduled</h1>
        <span className="text-xs text-fg-tertiary tabular-nums">{messages.length} {messages.length === 1 ? "message" : "messages"}</span>
      </div>
      <ul className="animate-content min-h-0 flex-1 overflow-y-auto">
        {messages.map((m) => (
          <li key={m.id} className="flex flex-col gap-2 border-b border-border px-4 py-3">
            <span className="flex items-center gap-2">
              <span className="flex-1 truncate font-semibold">{m.to.map((a) => a.name || a.address).join(", ") || "No recipients"}</span>
              <time dateTime={m.sendAt} className="shrink-0 text-xs text-fg-tertiary tabular-nums">
                {formatWhen(new Date(m.sendAt))}
              </time>
            </span>
            <span className="truncate text-sm text-fg-secondary">{m.subject || "(no subject)"}</span>
            {m.error && (
              <span className="flex items-center gap-2">
                <StatusBadge tone="danger">Not sent</StatusBadge>
                <span className="truncate text-xs text-fg-tertiary">{m.error}</span>
              </span>
            )}
            <Button variant="secondary" size="sm" className="self-start" loading={cancelling === m.id} onClick={() => onCancel(m.id)}>
              Cancel and edit
            </Button>
          </li>
        ))}
      </ul>
    </section>
  )
}

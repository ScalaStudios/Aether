"use client"

import { FolderOpen } from "lucide-react"
import { cn } from "@/lib/utils"

import { IconButton } from "@/components/icon-button"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { formatWhen } from "@/lib/format"
import { useI18n } from "@/lib/i18n"
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
  const { t, tp } = useI18n()

  return (
    <section aria-label={t("mail.scheduled.sectionLabel")} className={cn("min-h-0 flex-col border-r border-border", className)}>
      <div className="flex items-center gap-2 border-b border-border p-4">
        <IconButton label={t("mail.list.showFolders")} size="icon-sm" className="lg:hidden" onClick={onShowFolders}>
          <FolderOpen />
        </IconButton>
        <h1 className="flex-1 truncate text-h3">{t("mail.scheduled.title")}</h1>
        <span className="text-xs text-fg-tertiary tabular-nums">{tp("mail.messageCount", messages.length)}</span>
      </div>
      <ul className="animate-content min-h-0 flex-1 overflow-y-auto">
        {messages.map((m) => (
          <li key={m.id} className="flex flex-col gap-2 border-b border-border px-4 py-3">
            <span className="flex items-center gap-2">
              <span className="flex-1 truncate font-semibold">{m.to.map((a) => a.name || a.address).join(", ") || t("mail.noRecipients")}</span>
              <time dateTime={m.sendAt} className="shrink-0 text-xs text-fg-tertiary tabular-nums">
                {formatWhen(new Date(m.sendAt))}
              </time>
            </span>
            <span className="truncate text-sm text-fg-secondary">{m.subject || t("mail.noSubject")}</span>
            {m.error && (
              <span className="flex items-center gap-2">
                <StatusBadge tone="danger">{t("mail.scheduled.notSent")}</StatusBadge>
                <span className="truncate text-xs text-fg-tertiary">{m.error}</span>
              </span>
            )}
            <Button variant="secondary" size="sm" className="self-start" loading={cancelling === m.id} onClick={() => onCancel(m.id)}>
              {t("mail.scheduled.cancelAndEdit")}
            </Button>
          </li>
        ))}
      </ul>
    </section>
  )
}

"use client"

import { cn } from "@/lib/utils"

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { formatBytes, formatDateTime, formatListDate, plural } from "@/lib/format"
import type { MailboxKind } from "@/lib/types"

export function QuotaUsage({
  storageBytes = 0,
  quotaBytes = 0,
  messageCount,
  storageUpdatedAt,
}: {
  storageBytes?: number
  quotaBytes?: number
  messageCount?: number
  storageUpdatedAt?: string | null
}) {
  const pct = quotaBytes > 0 ? Math.round((storageBytes / quotaBytes) * 1000) / 10 : null
  const clamped = pct === null ? 0 : Math.min(100, Math.max(0, pct))
  const tone = pct !== null && pct >= 95 ? "bg-danger" : pct !== null && pct >= 80 ? "bg-warning" : "bg-primary"
  const pctTone = pct !== null && pct >= 95 ? "text-danger" : pct !== null && pct >= 80 ? "text-warning" : "text-fg-tertiary"

  const content = (
    <div className="flex min-w-36 flex-col gap-1">
      <div className="flex items-baseline justify-between gap-2 tabular-nums">
        <span className="font-medium">{formatBytes(storageBytes)}</span>
        <span className={cn("text-xs", pctTone)}>
          {quotaBytes > 0 ? `${pct}% of ${formatBytes(quotaBytes)}` : "No limit"}
        </span>
      </div>
      {quotaBytes > 0 && (
        <div
          role="progressbar"
          aria-label="Quota usage"
          aria-valuenow={Math.round(clamped)}
          aria-valuemin={0}
          aria-valuemax={100}
          className="h-1.5 w-full overflow-hidden rounded-full bg-muted"
        >
          <div className={cn("h-full rounded-full transition-all", tone)} style={{ width: `${clamped}%` }} />
        </div>
      )}
    </div>
  )

  if (messageCount === undefined && !storageUpdatedAt) return content

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div className="cursor-default">{content}</div>
      </TooltipTrigger>
      <TooltipContent className="flex flex-col gap-1">
        {messageCount !== undefined && <span>{plural(messageCount, "message")} stored</span>}
        <span className="text-xs opacity-80">
          {storageUpdatedAt ? `Measured ${formatDateTime(storageUpdatedAt)}` : "Not measured yet"}
        </span>
      </TooltipContent>
    </Tooltip>
  )
}

export function LastSignIn({
  kind,
  lastSignInAt,
  lastWebLoginAt,
  lastImapLoginAt,
  lastSmtpLoginAt,
}: {
  kind: MailboxKind
  lastSignInAt?: string | null
  lastWebLoginAt?: string | null
  lastImapLoginAt?: string | null
  lastSmtpLoginAt?: string | null
}) {
  if (kind === "shared") {
    return <span className="text-sm text-fg-tertiary">—</span>
  }
  const channels = [
    { label: "Web", value: lastWebLoginAt },
    { label: "IMAP", value: lastImapLoginAt },
    { label: "SMTP", value: lastSmtpLoginAt },
  ]
  const used = channels.filter((c) => !!c.value)

  if (!lastSignInAt && used.length === 0) {
    return <span className="text-sm text-fg-tertiary">Never</span>
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div className="flex cursor-default flex-col gap-0.5 tabular-nums">
          <span>{lastSignInAt ? formatListDate(lastSignInAt) : "Never"}</span>
          <span className="text-xs text-fg-tertiary">
            {used.map((c) => `${c.label} ${formatListDate(c.value!)}`).join(" · ")}
          </span>
        </div>
      </TooltipTrigger>
      <TooltipContent className="flex flex-col gap-1">
        {channels.map((c) => (
          <span key={c.label} className="tabular-nums">
            {c.label}: {c.value ? formatDateTime(c.value) : "Never"}
          </span>
        ))}
      </TooltipContent>
    </Tooltip>
  )
}

"use client"

import { CircleAlert, X } from "lucide-react"
import Link from "next/link"
import { useState } from "react"
import { cn } from "cn"

import { IconButton } from "@/components/icon-button"
import { Button } from "@/components/ui/button"
import { usagePercent } from "@/components/usage-bar"
import { storageTier } from "@/lib/mail"
import type { Folder, Mailbox } from "@/lib/types"

export function StorageBanner({ mailbox, folders }: { mailbox: Mailbox; folders?: Folder[] }) {
  const [dismissed, setDismissed] = useState<string | null>(null)
  if (!folders || !mailbox.quotaBytes) return null
  const used = folders.reduce((n, f) => n + f.size, 0)
  const tier = storageTier(used, mailbox.quotaBytes, mailbox.quotaWarningPercents)
  const key = `${mailbox.id}:${tier}`
  if (tier === "ok" || dismissed === key) return null
  const who = mailbox.access === "owner" ? "Your mailbox" : mailbox.address
  const percent = usagePercent(used, mailbox.quotaBytes)
  const message = {
    warning: `${who} is ${percent}% full. When it's full, new mail is returned to its senders.`,
    critical: `${who} is almost full (${percent}%). Free up space soon so new mail keeps arriving.`,
    full: `${who} is full. New mail is being returned to its senders until you free up space.`,
  }[tier]
  const danger = tier !== "warning"

  return (
    <div role="status" className={cn("animate-content flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-2 text-sm", danger ? "border-danger/40 bg-danger/12" : "border-warning/40 bg-warning/12")}>
      <CircleAlert aria-hidden className={cn("size-4 shrink-0", danger ? "text-danger" : "text-warning")} />
      <p className="min-w-0 flex-1 basis-48">{message}</p>
      <div className="flex items-center gap-1">
        <Button variant="secondary" size="sm" asChild>
          <Link href="/settings#storage">Free up space</Link>
        </Button>
        {tier !== "full" && (
          <IconButton label="Dismiss storage warning" size="icon-sm" onClick={() => setDismissed(key)}>
            <X />
          </IconButton>
        )}
      </div>
    </div>
  )
}

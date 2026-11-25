import { cn } from "cn"

import type { StorageTier } from "@/lib/mail"

const fills: Record<StorageTier, string> = {
  ok: "bg-fg-tertiary",
  warning: "bg-warning",
  critical: "bg-danger",
  full: "bg-danger",
}

export function usagePercent(used: number, quota: number) {
  return quota ? Math.floor((used / quota) * 100) : 0
}

export function UsageBar({ used, quota, tier, className }: { used: number; quota: number; tier: StorageTier; className?: string }) {
  return (
    <div aria-hidden className={cn("h-1 overflow-hidden rounded-full bg-muted", className)}>
      <div className={cn("h-full rounded-full transition-[width] duration-300 ease-brand", fills[tier])} style={{ width: `${Math.min(100, Math.max(1, (used / quota) * 100))}%` }} />
    </div>
  )
}

export const tierText: Record<StorageTier, string> = {
  ok: "text-fg-tertiary",
  warning: "text-warning",
  critical: "text-danger",
  full: "text-danger",
}

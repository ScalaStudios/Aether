"use client"

import { cn } from "cn"
import { useState, useSyncExternalStore } from "react"

import { apiUrl } from "@/lib/api"

let version = Date.now()
const listeners = new Set<() => void>()

export function refreshAvatars() {
  version = Date.now()
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function initials(source: string) {
  return source
    .split(/[\s@.]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase())
    .join("")
}

export function Avatar({ address, name, className }: { address: string; name?: string; className?: string }) {
  const v = useSyncExternalStore(subscribe, () => version, () => version)
  const src = apiUrl(`/avatars/${encodeURIComponent(address.toLowerCase())}`, { v })
  const [failed, setFailed] = useState("")

  return (
    <span
      aria-hidden
      className={cn("relative flex size-8 shrink-0 items-center justify-center overflow-hidden rounded-full bg-muted text-xs font-semibold text-foreground", className)}
    >
      {initials(name || address)}
      {failed !== src && <img src={src} alt="" onError={() => setFailed(src)} className="absolute inset-0 size-full object-cover" />}
    </span>
  )
}

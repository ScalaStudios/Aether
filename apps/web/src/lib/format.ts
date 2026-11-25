import { getActiveLocale, tActive } from "@/lib/i18n"
import type { Address, AttendeeStatus } from "@/lib/types"

export function formatAddress(a: Address) {
  return a.name ? `${a.name} <${a.address}>` : a.address
}

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} ${tActive("common.units.B")}`
  const units = ["KB", "MB", "GB", "TB"] as const
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${tActive(`common.units.${units[unit]}`)}`
}

export function attendeeStatusLabel(status: AttendeeStatus) {
  return tActive(`calendar.attendeeStatus.${status}`)
}

export const attendeeStatusLabels: Record<AttendeeStatus, string> = {
  "needs-action": "No response yet",
  accepted: "Going",
  tentative: "Maybe",
  declined: "Not going",
}

export function plural(count: number, noun: string, many = `${noun}s`) {
  return `${count.toLocaleString()} ${count === 1 ? noun : many}`
}

export function formatPercent(part: number, total: number) {
  return `${total ? Math.round((part / total) * 100) : 0}%`
}

export function formatDateTime(iso: string) {
  return new Intl.DateTimeFormat(getActiveLocale(), { day: "numeric", month: "long", year: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(iso))
}

const relativeUnits: [Intl.RelativeTimeFormatUnit, number][] = [
  ["day", 86_400],
  ["hour", 3_600],
  ["minute", 60],
]

export function formatRelative(iso: string) {
  const seconds = (new Date(iso).getTime() - Date.now()) / 1000
  const format = new Intl.RelativeTimeFormat(undefined, { numeric: "auto", style: "short" })
  const [unit, size] = relativeUnits.find(([, size]) => Math.abs(seconds) >= size) ?? ["second", 1]
  return format.format(Math.round(seconds / size), unit)
}

export function formatDate(iso: string) {
  return new Intl.DateTimeFormat(getActiveLocale(), { day: "numeric", month: "long", year: "numeric" }).format(new Date(iso))
}

export function formatListDate(iso: string) {
  const date = new Date(iso)
  const now = new Date()
  const locale = getActiveLocale()
  if (date.toDateString() === now.toDateString()) {
    return new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(date)
  }
  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    year: date.getFullYear() === now.getFullYear() ? undefined : "numeric",
  }).format(date)
}

export function formatPreciseDateTime(iso: string) {
  return new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(iso))
}

export function formatShortDateTime(date: Date) {
  return new Intl.DateTimeFormat(getActiveLocale(), { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(date)
}

export function formatWhen(date: Date) {
  const time = new Intl.DateTimeFormat(getActiveLocale(), { hour: "2-digit", minute: "2-digit" }).format(date)
  const days = Math.round((new Date(date).setHours(0, 0, 0, 0) - new Date().setHours(0, 0, 0, 0)) / 86_400_000)
  if (days === 0) return tActive("common.todayAt", { time })
  if (days === 1) return tActive("common.tomorrowAt", { time })
  return formatShortDateTime(date)
}

import { DoorOpen, Projector, type LucideIcon } from "lucide-react"

import type { Tone } from "@/components/status-badge"
import type { AttendeeStatus, Resource, ResourceChoice, ResourceType } from "@/lib/types"

export const resourceTypes: Record<ResourceType, { label: string; plural: string; icon: LucideIcon }> = {
  room: { label: "Room", plural: "Rooms", icon: DoorOpen },
  equipment: { label: "Equipment", plural: "Equipment", icon: Projector },
}

export const bookingStatus: Record<AttendeeStatus, { label: string; tone: Tone }> = {
  accepted: { label: "Booked", tone: "success" },
  tentative: { label: "Awaiting approval", tone: "info" },
  declined: { label: "Declined", tone: "danger" },
  "needs-action": { label: "Not booked yet", tone: "neutral" },
}

export const availabilityStatus: Record<NonNullable<ResourceChoice["availability"]>, { label: string; tone: Tone }> = {
  free: { label: "Available", tone: "success" },
  busy: { label: "Busy", tone: "warning" },
  restricted: { label: "Unavailable", tone: "danger" },
}

const pad = (n: number) => String(n).padStart(2, "0")

export function clock(minutes: number) {
  return `${pad(Math.floor(minutes / 60))}:${pad(minutes % 60)}`
}

export function fromClock(value: string) {
  const [h, m] = value.split(":").map(Number)
  return h * 60 + m
}

export function durationLabel(minutes: number) {
  const unit = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`
  if (minutes % 1440 === 0) return unit(minutes / 1440, "day")
  if (minutes % 60 === 0) return unit(minutes / 60, "hour")
  if (minutes > 60) return `${unit(Math.floor(minutes / 60), "hour")} ${unit(minutes % 60, "minute")}`
  return unit(minutes, "minute")
}

const weekdayFormat = new Intl.DateTimeFormat(undefined, { weekday: "short" })

export const weekdays = [1, 2, 3, 4, 5, 6, 0].map((day) => ({ value: day, label: weekdayFormat.format(new Date(2026, 0, 4 + day)) }))

function daysLabel(days: number[]) {
  const sorted = weekdays.filter((d) => days.includes(d.value))
  if (sorted.length === 7) return "Every day"
  const order = weekdays.map((d) => d.value)
  const contiguous = sorted.length >= 3 && sorted.every((d, i) => i === 0 || order.indexOf(d.value) === order.indexOf(sorted[i - 1].value) + 1)
  if (contiguous) return `${sorted[0].label}–${sorted[sorted.length - 1].label}`
  return sorted.map((d) => d.label).join(", ")
}

export function ruleSummary(r: Resource) {
  return [
    r.policy === "approval" && "Needs approval",
    r.maxMinutes && `Up to ${durationLabel(r.maxMinutes)}`,
    r.workingHours && `${daysLabel(r.workDays)} ${clock(r.workStart)}–${clock(r.workEnd)}`,
  ].filter((x): x is string => !!x)
}

export function resourceDetails(r: Pick<Resource, "location" | "capacity" | "type">) {
  return [r.location, r.type === "room" && r.capacity ? `${r.capacity} ${r.capacity === 1 ? "seat" : "seats"}` : ""].filter(Boolean).join(" · ")
}

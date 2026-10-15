import { useSyncExternalStore } from "react"

import type { CalendarEvent } from "@/lib/types"

const pad = (n: number) => String(n).padStart(2, "0")

export function addDays(date: Date, days: number) {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate() + days, date.getHours(), date.getMinutes())
}

export function startOfDay(date: Date) {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate())
}

export function startOfWeek(date: Date) {
  return addDays(startOfDay(date), -((date.getDay() + 6) % 7))
}

export function sameDay(a: Date, b: Date) {
  return a.toDateString() === b.toDateString()
}

export function dateValue(date: Date) {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

export function timeValue(date: Date) {
  return `${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function fromValues(date: string, time = "00:00") {
  const [y, m, d] = date.split("-").map(Number)
  const [h, min] = time.split(":").map(Number)
  return new Date(y, m - 1, d, h, min)
}

export function toRFC3339(date: Date) {
  const offset = -date.getTimezoneOffset()
  const abs = Math.abs(offset)
  return `${dateValue(date)}T${timeValue(date)}:00${offset < 0 ? "-" : "+"}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`
}

export function eventRange(event: CalendarEvent) {
  if (event.allDay) return { start: fromValues(event.start.slice(0, 10)), end: fromValues(event.end.slice(0, 10)) }
  return { start: new Date(event.start), end: new Date(event.end) }
}

export interface Placed {
  event: CalendarEvent
  start: Date
  end: Date
  top: number
  bottom: number
  column: number
  columns: number
}

export function layoutDay(events: CalendarEvent[], day: Date): Placed[] {
  const dayStart = day.getTime()
  const dayEnd = addDays(day, 1).getTime()
  const items = events
    .filter((e) => !e.allDay)
    .map((event) => ({ event, ...eventRange(event) }))
    .filter(({ start, end }) => start.getTime() < dayEnd && end.getTime() > dayStart)
    .map((item) => ({
      ...item,
      top: (Math.max(item.start.getTime(), dayStart) - dayStart) / 60_000,
      bottom: (Math.min(item.end.getTime(), dayEnd) - dayStart) / 60_000,
      column: 0,
      columns: 1,
    }))
    .sort((a, b) => a.top - b.top || b.bottom - a.bottom)

  let cluster: Placed[] = []
  let columnEnds: number[] = []
  let clusterEnd = -1
  const flush = () => {
    for (const item of cluster) item.columns = columnEnds.length
    cluster = []
    columnEnds = []
  }
  for (const item of items) {
    if (item.top >= clusterEnd) flush()
    let column = columnEnds.findIndex((end) => end <= item.top)
    if (column === -1) column = columnEnds.push(item.bottom) - 1
    else columnEnds[column] = item.bottom
    item.column = column
    cluster.push(item)
    clusterEnd = Math.max(clusterEnd, item.bottom)
  }
  flush()
  return items
}

export function eventsOn(events: CalendarEvent[], day: Date) {
  const next = addDays(day, 1)
  return events.filter((e) => {
    const { start, end } = eventRange(e)
    return start < next && end > day
  })
}

function subscribeMinute(onChange: () => void) {
  const timer = setInterval(onChange, 30_000)
  return () => clearInterval(timer)
}

export function useNow() {
  const minute = useSyncExternalStore(subscribeMinute, () => Math.floor(Date.now() / 60_000), () => null)
  return minute === null ? null : new Date(minute * 60_000)
}

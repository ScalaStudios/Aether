"use client"

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { CalendarCog, ChevronLeft, ChevronRight, Download, Ellipsis, Plus, Upload } from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { CalendarsDialog } from "@/components/calendar/calendars-dialog"
import { EventDialog, type EventSelection } from "@/components/calendar/event-dialog"
import { IconButton } from "@/components/icon-button"
import { SegmentedControl } from "@/components/segmented-control"
import { Spinner } from "@/components/spinner"
import { ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { api, apiUrl, errorMessage } from "@/lib/api"
import { plural } from "@/lib/format"
import { calendarsQuery } from "@/lib/queries"
import { addDays, eventRange, eventsOn, layoutDay, sameDay, startOfDay, startOfWeek, toRFC3339, useNow } from "@/lib/calendar"
import type { CalendarEvent } from "@/lib/types"

const HOUR = 48
const weekColumns = "grid grid-cols-[56px_repeat(7,minmax(0,1fr))]"

const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" })
const hourFormat = new Intl.DateTimeFormat(undefined, { hour: "numeric" })
const weekdayFormat = new Intl.DateTimeFormat(undefined, { weekday: "short" })
const dayFormat = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long" })
const rangeFormat = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "long", year: "numeric" })
const monthFormat = new Intl.DateTimeFormat(undefined, { month: "long", year: "numeric" })

type View = "week" | "month"
type Open = (event: CalendarEvent) => void

function timeLabel(event: CalendarEvent) {
  if (event.allDay) return "All day"
  const { start, end } = eventRange(event)
  return timeFormat.formatRange(start, end)
}

function EventButton({ event, onOpen, detail, className, style }: { event: CalendarEvent; onOpen: Open; detail?: boolean; className?: string; style?: React.CSSProperties }) {
  return (
    <button
      type="button"
      onClick={() => onOpen(event)}
      style={{ ...style, "--event": event.color || "var(--color-ember)" } as React.CSSProperties}
      className={cn(
        "flex min-w-0 cursor-pointer flex-col overflow-hidden rounded-md border-l-2 border-(--event) bg-card bg-linear-to-r from-(--event)/16 to-(--event)/16 px-2 py-1 text-left text-xs leading-4 text-foreground transition-colors duration-160 ease-brand hover:from-(--event)/28 hover:to-(--event)/28",
        className
      )}
    >
      <span className="truncate font-semibold">{event.title}</span>
      <span className={cn("truncate text-fg-secondary", !detail && "sr-only")}>
        {timeLabel(event)}
        {event.location && `, ${event.location}`}
      </span>
    </button>
  )
}

export function CalendarView() {
  const now = useNow()
  if (!now) return <RowsSkeleton rows={6} className="p-6" />
  return <Calendar now={now} />
}

function CalendarMenu({ onManage }: { onManage: () => void }) {
  const qc = useQueryClient()
  const fileInput = useRef<HTMLInputElement>(null)
  const importFile = useMutation({
    mutationFn: (file: File) => api<{ imported: number; updated: number; skipped: number }>("/calendar/import", { method: "POST", body: file }),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
      toast.success(`Imported ${plural(r.imported + r.updated, "event")}`, { description: r.skipped ? `Skipped ${plural(r.skipped, "event")} that couldn't be read.` : undefined })
    },
    onError: (error) => toast.error(errorMessage(error)),
  })

  return (
    <>
      <input
        ref={fileInput}
        type="file"
        accept=".ics,text/calendar"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0]
          if (file) importFile.mutate(file)
          e.target.value = ""
        }}
      />
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="secondary" size="icon" aria-label="Calendar options" loading={importFile.isPending}>
            <Ellipsis />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuItem onSelect={onManage}>
            <CalendarCog aria-hidden />
            Calendars and sharing
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => fileInput.current?.click()}>
            <Upload aria-hidden />
            Import .ics file
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <a href={apiUrl("/calendar/export")} download="calendar.ics">
              <Download aria-hidden />
              Export .ics file
            </a>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  )
}

function Calendar({ now }: { now: Date }) {
  const [view, setView] = useState<View>("week")
  const [anchor, setAnchor] = useState<Date | null>(null)
  const [selection, setSelection] = useState<EventSelection | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)

  const day = anchor ?? startOfDay(now)
  const start = view === "week" ? startOfWeek(day) : startOfWeek(new Date(day.getFullYear(), day.getMonth(), 1))
  const days = Array.from({ length: view === "week" ? 7 : 42 }, (_, i) => addDays(start, i))
  const end = addDays(start, days.length)

  const events = useQuery({
    queryKey: ["calendar-events", toRFC3339(start), toRFC3339(end)],
    queryFn: () => api<CalendarEvent[]>("/calendar/events", { query: { start: toRFC3339(start), end: toRFC3339(end) } }),
    placeholderData: keepPreviousData,
  })
  const calendars = useQuery(calendarsQuery)
  const [managing, setManaging] = useState(false)
  const [hidden, setHidden] = useState<string[]>(() => JSON.parse(localStorage.getItem("aether.hiddenCalendars") ?? "[]"))
  const colors = new Map(calendars.data?.map((c) => [c.id, c.color]))
  const list = (events.data ?? []).filter((e) => !hidden.includes(e.calendarId ?? "")).map((e) => ({ ...e, color: colors.get(e.calendarId ?? "") }))

  function toggleCalendar(id: string) {
    const next = hidden.includes(id) ? hidden.filter((h) => h !== id) : [...hidden, id]
    setHidden(next)
    localStorage.setItem("aether.hiddenCalendars", JSON.stringify(next))
  }

  function move(delta: number) {
    setAnchor(view === "week" ? addDays(day, 7 * delta) : new Date(day.getFullYear(), day.getMonth() + delta, 1))
  }

  function open(selected: Omit<EventSelection, "key">) {
    setSelection({ key: Date.now(), ...selected })
    setDialogOpen(true)
  }

  function openEvent(event: CalendarEvent) {
    open({ event, allDay: event.allDay, ...eventRange(event) })
  }

  function create(at: Date, allDay = false) {
    open({ start: at, end: allDay ? addDays(at, 1) : new Date(at.getTime() + 3_600_000), allDay })
  }

  function newEvent() {
    const inView = now >= start && now < end
    create(inView ? new Date(now.getFullYear(), now.getMonth(), now.getDate(), now.getHours() + 1) : new Date(day.getFullYear(), day.getMonth(), day.getDate(), 9))
  }

  function showDay(date: Date) {
    setView("week")
    setAnchor(date)
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-4 md:px-6">
        <div className="flex items-center gap-1">
          <Button variant="secondary" size="sm" className="mr-2 max-md:min-h-11" onClick={() => setAnchor(null)}>
            Today
          </Button>
          <IconButton label={view === "week" ? "Previous week" : "Previous month"} size="icon-sm" onClick={() => move(-1)}>
            <ChevronLeft />
          </IconButton>
          <IconButton label={view === "week" ? "Next week" : "Next month"} size="icon-sm" onClick={() => move(1)}>
            <ChevronRight />
          </IconButton>
        </div>
        <h1 aria-live="polite" className="min-w-0 flex-1 truncate text-h3 max-md:order-first max-md:basis-full">
          {view === "week" ? rangeFormat.formatRange(start, addDays(start, 6)) : monthFormat.format(day)}
        </h1>
        {events.isFetching && <Spinner />}
        <SegmentedControl
          label="View"
          options={[
            { value: "week", label: "Week" },
            { value: "month", label: "Month" },
          ]}
          value={view}
          onChange={setView}
        />
        <CalendarMenu onManage={() => setManaging(true)} />
        <Button onClick={newEvent}>
          <Plus aria-hidden />
          New event
        </Button>
      </header>
      {events.isError ? (
        <ErrorState error={events.error} onRetry={() => events.refetch()} className="flex-1" />
      ) : view === "week" ? (
        <>
          <WeekGrid days={days} events={list} now={now} busy={events.isPending} onOpen={openEvent} onCreate={create} />
          <Agenda days={days} events={list} now={now} busy={events.isPending} onOpen={openEvent} />
        </>
      ) : (
        <MonthGrid days={days} month={day.getMonth()} events={list} now={now} busy={events.isPending} onOpen={openEvent} onCreate={create} onShowDay={showDay} />
      )}
      {selection && <EventDialog key={selection.key} target={selection} calendars={calendars.data ?? []} open={dialogOpen} onOpenChange={setDialogOpen} />}
      <CalendarsDialog open={managing} onOpenChange={setManaging} hidden={hidden} onToggle={toggleCalendar} />
    </div>
  )
}

interface GridProps {
  days: Date[]
  events: CalendarEvent[]
  now: Date
  busy: boolean
  onOpen: Open
}

function WeekGrid({ days, events, now, busy, onOpen, onCreate }: GridProps & { onCreate: (at: Date, allDay?: boolean) => void }) {
  const scroller = useRef<HTMLDivElement>(null)
  useEffect(() => {
    scroller.current?.scrollTo({ top: 8 * HOUR - 16 })
  }, [])

  return (
    <div ref={scroller} aria-busy={busy} className="min-h-0 flex-1 overflow-y-auto max-md:hidden">
      <div className="sticky top-0 z-20 border-b border-border bg-background">
        <div className={weekColumns}>
          <div />
          {days.map((d) => (
            <div key={d.getTime()} className="flex flex-col items-center gap-1 border-l border-border py-2">
              <span className="text-xs text-fg-tertiary">{weekdayFormat.format(d)}</span>
              <span
                aria-current={sameDay(d, now) ? "date" : undefined}
                className={cn("flex size-8 items-center justify-center rounded-full font-semibold tabular-nums", sameDay(d, now) && "bg-ember text-ink")}
              >
                {d.getDate()}
              </span>
            </div>
          ))}
        </div>
        <div className={cn(weekColumns, "border-t border-border")}>
          <div className="py-2 pr-2 text-right text-xs text-fg-tertiary">All day</div>
          {days.map((d) => (
            <div key={d.getTime()} className="flex min-h-10 min-w-0 flex-col gap-1 border-l border-border p-1" onClick={(e) => e.target === e.currentTarget && onCreate(d, true)}>
              {eventsOn(events, d)
                .filter((e) => e.allDay)
                .map((e) => (
                  <EventButton key={`${e.id}-${e.start}`} event={e} onOpen={onOpen} />
                ))}
            </div>
          ))}
        </div>
      </div>
      <div className={cn(weekColumns, "relative")} style={{ height: 24 * HOUR }}>
        <div className="relative">
          {Array.from({ length: 23 }, (_, i) => (
            <span key={i} className="absolute right-2 -translate-y-1/2 text-xs text-fg-tertiary tabular-nums" style={{ top: (i + 1) * HOUR }}>
              {hourFormat.format(new Date(2000, 0, 1, i + 1))}
            </span>
          ))}
        </div>
        {days.map((d) => (
          <div
            key={d.getTime()}
            className="relative border-l border-border"
            style={{ backgroundImage: "linear-gradient(var(--border) 1px, transparent 1px)", backgroundSize: `100% ${HOUR}px` }}
            onClick={(e) => {
              if (e.target !== e.currentTarget) return
              const minutes = Math.floor(e.nativeEvent.offsetY / (HOUR / 2)) * 30
              onCreate(new Date(d.getFullYear(), d.getMonth(), d.getDate(), 0, minutes))
            }}
          >
            {layoutDay(events, d).map((p) => (
              <EventButton
                key={`${p.event.id}-${p.event.start}`}
                event={p.event}
                onOpen={onOpen}
                detail={p.bottom - p.top >= 45}
                className="absolute"
                style={{
                  top: (p.top / 60) * HOUR,
                  height: Math.max(((p.bottom - p.top) / 60) * HOUR, 20),
                  left: `calc(${(p.column / p.columns) * 100}% + 2px)`,
                  width: `calc(${100 / p.columns}% - 4px)`,
                }}
              />
            ))}
            {sameDay(d, now) && (
              <div aria-hidden className="pointer-events-none absolute inset-x-0 z-10 h-0.5 bg-ember" style={{ top: ((now.getHours() * 60 + now.getMinutes()) / 60) * HOUR }}>
                <span className="absolute -top-1 -left-1 size-2.5 rounded-full bg-ember" />
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

function Agenda({ days, events, now, busy, onOpen }: GridProps) {
  return (
    <ol aria-busy={busy} className="min-h-0 flex-1 overflow-y-auto md:hidden">
      {days.map((d) => {
        const list = eventsOn(events, d)
        return (
          <li key={d.getTime()} className="flex flex-col gap-2 border-b border-border px-4 py-4">
            <h2 className="flex items-center gap-2 text-label text-fg-secondary">
              {dayFormat.format(d)}
              {sameDay(d, now) && <span className="rounded-full bg-ember px-2 text-ink">Today</span>}
            </h2>
            {list.length ? (
              list.map((e) => <EventButton key={`${e.id}-${e.start}`} event={e} onOpen={onOpen} detail className="min-h-11 justify-center text-sm leading-5" />)
            ) : (
              <p className="text-sm text-fg-tertiary">No events</p>
            )}
          </li>
        )
      })}
    </ol>
  )
}

function MonthGrid({
  days,
  month,
  events,
  now,
  busy,
  onOpen,
  onCreate,
  onShowDay,
}: GridProps & { month: number; onCreate: (at: Date) => void; onShowDay: (day: Date) => void }) {
  return (
    <div
      aria-busy={busy}
      className="grid min-h-0 flex-1 grid-cols-7 grid-rows-[auto_repeat(6,minmax(64px,1fr))] overflow-y-auto md:grid-rows-[auto_repeat(6,minmax(128px,1fr))]"
    >
      {days.slice(0, 7).map((d) => (
        <div key={d.getTime()} className="border-b border-border py-2 text-center text-xs text-fg-tertiary">
          {weekdayFormat.format(d)}
        </div>
      ))}
      {days.map((d, i) => {
        const list = eventsOn(events, d)
        const today = sameDay(d, now)
        return (
          <div
            key={d.getTime()}
            className={cn("flex min-w-0 flex-col gap-1 border-b border-border p-1", i % 7 !== 0 && "border-l", d.getMonth() !== month && "bg-sunken")}
            onClick={(e) => e.target === e.currentTarget && onCreate(new Date(d.getFullYear(), d.getMonth(), d.getDate(), 9))}
          >
            <button
              type="button"
              aria-label={`${dayFormat.format(d)}, ${list.length === 1 ? "1 event" : `${list.length || "no"} events`}`}
              aria-current={today ? "date" : undefined}
              onClick={() => onShowDay(d)}
              className={cn(
                "flex size-7 shrink-0 cursor-pointer items-center justify-center self-center rounded-full text-sm tabular-nums transition-colors duration-160 hover:bg-muted max-md:size-11 md:self-start",
                d.getMonth() !== month && "text-fg-tertiary",
                today && "bg-ember font-semibold text-ink hover:bg-ember-hover"
              )}
            >
              {d.getDate()}
            </button>
            <div className="flex min-w-0 flex-col gap-1 max-md:hidden">
              {list.slice(0, 3).map((e) => (
                <EventButton key={`${e.id}-${e.start}`} event={e} onOpen={onOpen} />
              ))}
              {list.length > 3 && (
                <button type="button" onClick={() => onShowDay(d)} className="cursor-pointer self-start rounded-sm px-2 text-xs font-semibold text-fg-secondary hover:text-foreground">
                  +{list.length - 3} more
                </button>
              )}
            </div>
            {list.length > 0 && (
              <span aria-hidden className="self-center rounded-full bg-muted px-2 text-xs text-fg-secondary md:hidden">
                {list.length}
              </span>
            )}
          </div>
        )
      })}
    </div>
  )
}

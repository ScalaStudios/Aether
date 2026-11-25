"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { CalendarX, CircleCheck, Clock, ExternalLink, MapPin, Video } from "lucide-react"
import { useEffect, useState } from "react"
import { useForm } from "react-hook-form"
import { z } from "zod"

import { Field } from "@/components/field"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { ApiError, api } from "@/lib/api"
import { addDays, dateValue, startOfDay, toRFC3339, useNow } from "@/lib/calendar"
import type { BookingConfirmation, BookingSlots, PublicBookingPage, PublicBookingType } from "@/lib/types"
import { cn } from "cn"

const guestSchema = z.object({
  name: z.string().trim().min(1, "Enter your name.").max(200, "Use 200 characters or fewer."),
  email: z.email("Enter a valid email address."),
  note: z.string().max(2000, "Use 2,000 characters or fewer."),
})

type GuestValues = z.infer<typeof guestSchema>

const dayFormat = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric" })
const weekdayFormat = new Intl.DateTimeFormat(undefined, { weekday: "short" })
const dateFormat = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" })
const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" })
const confirmationFormat = new Intl.DateTimeFormat(undefined, { weekday: "long", month: "long", day: "numeric", hour: "numeric", minute: "2-digit" })

function durationLabel(minutes: number) {
  if (minutes % 60 === 0 && minutes >= 60) return minutes === 60 ? "1 hour" : `${minutes / 60} hours`
  if (minutes > 60) return `${Math.floor(minutes / 60)} h ${minutes % 60} min`
  return `${minutes} minutes`
}

function zoneLabel(zone: string) {
  try {
    const name = new Intl.DateTimeFormat(undefined, { timeZone: zone, timeZoneName: "shortOffset" }).formatToParts(new Date()).find((part) => part.type === "timeZoneName")?.value
    return name ? `${zone.replaceAll("_", " ")} (${name})` : zone.replaceAll("_", " ")
  } catch {
    return zone.replaceAll("_", " ")
  }
}

function groupSlots(slots: string[]) {
  const groups = new Map<string, string[]>()
  for (const slot of slots) {
    const key = dateValue(new Date(slot))
    groups.set(key, [...(groups.get(key) ?? []), slot])
  }
  return [...groups.entries()].map(([key, times]) => ({ key, times }))
}

export function PublicBooking({ slug }: { slug: string }) {
  const now = useNow()
  const page = useQuery({
    queryKey: ["public-booking", slug],
    queryFn: () => api<PublicBookingPage>(`/book/${encodeURIComponent(slug)}`),
  })
  const [typeSlug, setTypeSlug] = useState<string | null>(null)
  const [offset, setOffset] = useState(0)
  const [slot, setSlot] = useState<string | null>(null)
  const [confirmation, setConfirmation] = useState<BookingConfirmation | null>(null)
  const selectedSlug = page.data?.types.some((type) => type.slug === typeSlug) ? typeSlug : page.data?.types[0]?.slug ?? null

  useEffect(() => {
    const title = page.data ? `${page.data.title} · Aether` : "Book a meeting · Aether"
    if (document.title !== title) document.title = title
  }, [page.data])

  if (!now || page.isPending) {
    return (
      <main className="mx-auto flex min-h-dvh w-full max-w-3xl flex-col gap-6 px-5 py-8 md:py-12">
        <RowsSkeleton rows={6} />
      </main>
    )
  }
  if (page.isError) {
    const missing = page.error instanceof ApiError && page.error.status === 404
    return (
      <main className="mx-auto flex min-h-dvh w-full max-w-3xl items-center px-5 py-8">
        {missing ? (
          <EmptyState icon={CalendarX} title="This booking page isn't available" description="The link may be wrong, or the host turned the page off." />
        ) : (
          <ErrorState error={page.error} onRetry={() => page.refetch()} />
        )}
      </main>
    )
  }

  const data = page.data
  const selected = data.types.find((type) => type.slug === selectedSlug) ?? null
  const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone
  const sameZone = localZone === data.timezone

  function chooseType(next: string) {
    setTypeSlug(next)
    setSlot(null)
    setOffset(0)
    setConfirmation(null)
  }

  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-3xl flex-col gap-8 px-5 py-8 md:py-12">
      <header className="flex flex-col gap-2 border-b border-border pb-6">
        <p className="text-sm font-semibold text-primary">Book a time</p>
        <h1 className="text-h2">{data.title}</h1>
        <p className="text-fg-secondary">with {data.hostName}</p>
        {data.description && <p className="max-w-[62ch] text-fg-secondary">{data.description}</p>}
      </header>
      {confirmation ? (
        <Confirmation confirmation={confirmation} onAgain={() => { setConfirmation(null); setSlot(null) }} />
      ) : data.types.length === 0 ? (
        <EmptyState icon={CalendarX} title="Nothing to book yet" description="This host hasn't published a meeting type." />
      ) : (
        <>
          <section className="flex flex-col gap-3">
            <h2 className="text-h3">{data.types.length === 1 ? "Meeting" : "Choose a meeting"}</h2>
            <div role="radiogroup" aria-label="Meeting type" className="grid gap-2">
              {data.types.map((type) => {
                const active = type.slug === selected?.slug
                return (
                  <button
                    key={type.slug}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    onClick={() => chooseType(type.slug)}
                    className={cn(
                      "flex cursor-pointer flex-col gap-1.5 rounded-xl border px-4 py-3 text-left transition-colors duration-160",
                      active ? "border-primary bg-primary/10" : "border-border bg-card hover:border-strong"
                    )}
                  >
                    <span className="font-semibold">{type.name}</span>
                    <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-fg-secondary">
                      <span className="inline-flex items-center gap-1">
                        <Clock aria-hidden className="size-3.5 text-fg-tertiary" />
                        {durationLabel(type.durationMinutes)}
                      </span>
                      {type.location && (
                        <span className="inline-flex items-center gap-1">
                          <MapPin aria-hidden className="size-3.5 text-fg-tertiary" />
                          {type.location}
                        </span>
                      )}
                      {type.video && (
                        <span className="inline-flex items-center gap-1">
                          <Video aria-hidden className="size-3.5 text-fg-tertiary" />
                          Video link after booking
                        </span>
                      )}
                    </span>
                    {type.description && <span className="text-sm text-fg-tertiary">{type.description}</span>}
                  </button>
                )
              })}
            </div>
          </section>
          {selected && (
            <SlotPicker
              slug={data.slug}
              type={selected}
              horizonDays={data.horizonDays || 180}
              now={now}
              offset={offset}
              slot={slot}
              sameZone={sameZone}
              hostZone={zoneLabel(data.timezone)}
              onOffset={(next) => { setOffset(next); setSlot(null) }}
              onSlot={setSlot}
              onBooked={setConfirmation}
            />
          )}
        </>
      )}
    </main>
  )
}

function SlotPicker({
  slug,
  type,
  horizonDays,
  now,
  offset,
  slot,
  sameZone,
  hostZone,
  onOffset,
  onSlot,
  onBooked,
}: {
  slug: string
  type: PublicBookingType
  horizonDays: number
  now: Date
  offset: number
  slot: string | null
  sameZone: boolean
  hostZone: string
  onOffset: (offset: number) => void
  onSlot: (slot: string | null) => void
  onBooked: (confirmation: BookingConfirmation) => void
}) {
  const from = addDays(startOfDay(now), offset)
  const to = addDays(from, 14)
  const slots = useQuery({
    queryKey: ["public-slots", slug, type.slug, toRFC3339(from), toRFC3339(to)],
    queryFn: () => api<BookingSlots>(`/book/${encodeURIComponent(slug)}/${encodeURIComponent(type.slug)}/slots`, { query: { start: toRFC3339(from), end: toRFC3339(to) } }),
  })
  const days = groupSlots(slots.data?.slots ?? [])
  const [day, setDay] = useState("")
  const selectedDay = days.some((entry) => entry.key === day) ? day : days[0]?.key ?? ""
  const times = days.find((entry) => entry.key === selectedDay)?.times ?? []
  const atEnd = offset + 14 >= horizonDays

  return (
    <>
      <section className="flex flex-col gap-3">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <h2 className="text-h3">Choose a time</h2>
          <div className="flex gap-2">
            <Button type="button" variant="secondary" size="sm" className="max-md:min-h-11" onClick={() => onOffset(Math.max(0, offset - 14))} disabled={offset === 0}>
              Earlier
            </Button>
            <Button type="button" variant="secondary" size="sm" className="max-md:min-h-11" onClick={() => onOffset(offset + 14)} disabled={atEnd}>
              Later
            </Button>
          </div>
        </div>
        <p className="text-sm text-fg-tertiary">
          {dayFormat.formatRange(from, addDays(to, -1))}
          {sameZone ? ` · ${hostZone}` : ` · Times are in your time zone. ${hostZone} is the host's.`}
        </p>
        {slots.isPending ? (
          <RowsSkeleton rows={3} />
        ) : slots.isError ? (
          <ErrorState error={slots.error} onRetry={() => slots.refetch()} />
        ) : days.length === 0 ? (
          <p className="rounded-xl border border-border bg-card px-4 py-6 text-fg-secondary">
            {atEnd ? "No open times in this range." : "No open times in these two weeks. Try a later range."}
          </p>
        ) : (
          <>
            <div role="radiogroup" aria-label="Day" className="flex gap-2 overflow-x-auto pb-1">
              {days.map((entry) => {
                const date = new Date(entry.times[0])
                const pressed = entry.key === selectedDay
                return (
                  <button
                    key={entry.key}
                    type="button"
                    role="radio"
                    aria-checked={pressed}
                    onClick={() => {
                      setDay(entry.key)
                      if (slot && !entry.times.includes(slot)) onSlot(null)
                    }}
                    className={cn(
                      "flex min-h-11 min-w-16 shrink-0 cursor-pointer flex-col items-center justify-center rounded-xl border px-3 py-2 transition-colors duration-160",
                      pressed ? "border-primary bg-primary text-primary-foreground" : "border-border bg-card hover:border-strong"
                    )}
                  >
                    <span className="text-xs font-medium">{weekdayFormat.format(date)}</span>
                    <span className="font-semibold tabular-nums">{dateFormat.format(date)}</span>
                  </button>
                )
              })}
            </div>
            <div role="radiogroup" aria-label="Time" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {times.map((time) => (
                <button
                  key={time}
                  type="button"
                  role="radio"
                  aria-checked={time === slot}
                  onClick={() => onSlot(time)}
                  className={cn(
                    "min-h-11 cursor-pointer rounded-xl border px-3 font-semibold tabular-nums transition-colors duration-160",
                    time === slot ? "border-primary bg-primary text-primary-foreground" : "border-border bg-card hover:border-strong"
                  )}
                >
                  {timeFormat.format(new Date(time))}
                </button>
              ))}
            </div>
          </>
        )}
      </section>
      {slot && <GuestForm slug={slug} type={type} slot={slot} onConflict={() => onSlot(null)} onBooked={onBooked} />}
    </>
  )
}

function GuestForm({
  slug,
  type,
  slot,
  onConflict,
  onBooked,
}: {
  slug: string
  type: PublicBookingType
  slot: string
  onConflict: () => void
  onBooked: (confirmation: BookingConfirmation) => void
}) {
  const qc = useQueryClient()
  const form = useForm<GuestValues>({ resolver: zodResolver(guestSchema), defaultValues: { name: "", email: "", note: "" } })
  const book = useMutation({
    mutationFn: (values: GuestValues) => api<BookingConfirmation>(`/book/${encodeURIComponent(slug)}/${encodeURIComponent(type.slug)}`, {
      method: "POST",
      body: { name: values.name.trim(), email: values.email.trim(), note: values.note.trim(), start: slot },
    }),
    onSuccess: (confirmation) => {
      qc.invalidateQueries({ queryKey: ["public-slots", slug] })
      onBooked(confirmation)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409) {
        qc.invalidateQueries({ queryKey: ["public-slots", slug] })
        onConflict()
      }
    },
  })
  const when = confirmationFormat.format(new Date(slot))

  return (
    <form className="flex flex-col gap-4 rounded-2xl border border-border bg-card p-4 md:p-6" noValidate onSubmit={form.handleSubmit((values) => book.mutate(values))}>
      <h2 className="text-h3">Your details</h2>
      <p className="text-fg-secondary">{type.name} · {when}</p>
      <Field id="guest-name" label="Name" error={form.formState.errors.name?.message}>
        <Input id="guest-name" autoComplete="name" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
      </Field>
      <Field id="guest-email" label="Email" hint="The invitation is sent here." error={form.formState.errors.email?.message}>
        <Input id="guest-email" type="email" autoComplete="email" aria-invalid={!!form.formState.errors.email} {...form.register("email")} />
      </Field>
      <Field id="guest-note" label="Note" hint="Optional. The host sees this on the event." error={form.formState.errors.note?.message}>
        <Textarea id="guest-note" className="min-h-24" {...form.register("note")} />
      </Field>
      <Button type="submit" loading={book.isPending}>Book {timeFormat.format(new Date(slot))}</Button>
    </form>
  )
}

function Confirmation({ confirmation, onAgain }: { confirmation: BookingConfirmation; onAgain: () => void }) {
  const when = confirmationFormat.formatRange(new Date(confirmation.start), new Date(confirmation.end))
  return (
    <section role="status" className="flex flex-col items-start gap-4 rounded-2xl border border-border bg-card p-6">
      <CircleCheck aria-hidden className="size-8 text-success" />
      <h2 className="text-h2">You’re booked</h2>
      <p className="text-fg-secondary">{confirmation.title}</p>
      <p className="font-semibold">{when}</p>
      {confirmation.location && confirmation.location !== confirmation.videoUrl && <p>{confirmation.location}</p>}
      {confirmation.videoUrl && (
        <a
          className="inline-flex items-center gap-1.5 font-medium text-link underline underline-offset-3"
          href={confirmation.videoUrl}
          target="_blank"
          rel="noopener noreferrer"
        >
          Join the video call
          <ExternalLink aria-hidden className="size-4" />
        </a>
      )}
      <p className="max-w-[52ch] text-fg-secondary">An invitation is on its way to {confirmation.guestEmail}. Add it to your calendar from that email.</p>
      <Button type="button" variant="secondary" onClick={onAgain}>Book another time</Button>
    </section>
  )
}

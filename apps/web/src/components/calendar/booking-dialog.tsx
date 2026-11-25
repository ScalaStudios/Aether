"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ExternalLink, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useFieldArray, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyField } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SwitchField } from "@/components/switch-field"
import { QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { calendarsQuery } from "@/lib/queries"
import type { BookingPage, BookingType, BookingWindow, CalendarInfo, Session } from "@/lib/types"

const weekdays = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]
const shortDays = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]

const pageSchema = z.object({
  slug: z
    .string()
    .trim()
    .toLowerCase()
    .regex(/^[a-z0-9][a-z0-9-]{0,62}$/, "Use lowercase letters, numbers and hyphens."),
  title: z.string().trim().min(1, "Give the page a title.").max(200, "Use 200 characters or fewer."),
  description: z.string().max(2000, "Use 2,000 characters or fewer."),
  timezone: z.string().trim().min(1, "Choose a time zone.").max(100, "Use a shorter time zone name."),
  minNoticeMinutes: z.string().trim().regex(/^\d+$/, "Enter a whole number of minutes."),
  horizonDays: z.string().trim().regex(/^\d+$/, "Enter a whole number of days."),
  active: z.boolean(),
}).superRefine((value, ctx) => {
  const notice = Number(value.minNoticeMinutes)
  const horizon = Number(value.horizonDays)
  if (notice > 10080) ctx.addIssue({ code: "custom", message: "Notice can be at most 7 days.", path: ["minNoticeMinutes"] })
  if (horizon < 1 || horizon > 180) ctx.addIssue({ code: "custom", message: "People can book from 1 to 180 days ahead.", path: ["horizonDays"] })
})

const typeSchema = z.object({
  name: z.string().trim().min(1, "Give the meeting a name.").max(100, "Use 100 characters or fewer."),
  description: z.string().max(2000, "Use 2,000 characters or fewer."),
  durationMinutes: z.string().trim().regex(/^\d+$/, "Enter the length in minutes."),
  bufferBeforeMinutes: z.string().trim().regex(/^\d+$/, "Enter a whole number of minutes."),
  bufferAfterMinutes: z.string().trim().regex(/^\d+$/, "Enter a whole number of minutes."),
  location: z.string().trim().max(500, "Use 500 characters or fewer."),
  videoUrl: z.string().trim().refine((value) => value === "" || /^https:\/\/\S+$/i.test(value), "Use an https:// link."),
  calendarId: z.string().min(1, "Choose a calendar."),
  active: z.boolean(),
  windows: z.array(z.object({
    weekday: z.number().int().min(0).max(6),
    start: z.string().min(1, "Choose a start."),
    end: z.string().min(1, "Choose an end."),
  })).min(1, "Add the hours you can meet.").max(21, "Use 21 windows or fewer."),
}).superRefine((value, ctx) => {
  const duration = Number(value.durationMinutes)
  const before = Number(value.bufferBeforeMinutes)
  const after = Number(value.bufferAfterMinutes)
  if (!Number.isFinite(duration) || duration < 5 || duration > 480 || duration % 5 !== 0) {
    ctx.addIssue({ code: "custom", message: "Duration is between 5 minutes and 8 hours, in steps of 5.", path: ["durationMinutes"] })
  }
  if (!Number.isFinite(before) || before > 240 || before % 5 !== 0) {
    ctx.addIssue({ code: "custom", message: "Use 0 to 240 minutes, in steps of 5.", path: ["bufferBeforeMinutes"] })
  }
  if (!Number.isFinite(after) || after > 240 || after % 5 !== 0) {
    ctx.addIssue({ code: "custom", message: "Use 0 to 240 minutes, in steps of 5.", path: ["bufferAfterMinutes"] })
  }
  const problem = windowProblem(value.windows, duration)
  if (problem) {
    ctx.addIssue({ code: "custom", message: problem, path: ["windows"] })
  }
})

type PageValues = z.infer<typeof pageSchema>
type TypeValues = z.infer<typeof typeSchema>

function clock(minutes: number) {
  return `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`
}

function toMinutes(value: string) {
  const match = /^(\d{1,2}):(\d{2})/.exec(value)
  if (!match) return Number.NaN
  return Number(match[1]) * 60 + Number(match[2])
}

function durationLabel(minutes: number) {
  if (minutes % 60 === 0 && minutes >= 60) return minutes === 60 ? "1 hour" : `${minutes / 60} hours`
  if (minutes > 60) return `${Math.floor(minutes / 60)} h ${minutes % 60} min`
  return `${minutes} minutes`
}

function hoursSummary(windows: BookingWindow[]) {
  const byDay = new Map<number, string>()
  for (const w of windows) {
    const span = `${clock(w.startMinute)}–${clock(w.endMinute)}`
    byDay.set(w.weekday, byDay.has(w.weekday) ? `${byDay.get(w.weekday)}, ${span}` : span)
  }
  const days = [...byDay.entries()].sort((a, b) => a[0] - b[0])
  const groups: { start: number; end: number; hours: string }[] = []
  for (const [day, hours] of days) {
    const prev = groups.at(-1)
    if (prev && prev.hours === hours && prev.end + 1 === day) {
      prev.end = day
    } else {
      groups.push({ start: day, end: day, hours })
    }
  }
  return groups
    .map((g) => (g.start === g.end ? `${shortDays[g.start]} ${g.hours}` : `${shortDays[g.start]}–${shortDays[g.end]} ${g.hours}`))
    .join(", ")
}

function suggestSlug(value: string) {
  const slug = value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63)
  return /^[a-z0-9]/.test(slug) ? slug : "meet"
}

function windowProblem(windows: TypeValues["windows"], duration: number) {
  const spans: { weekday: number; start: number; end: number }[] = []
  let fits = false
  for (const window of windows) {
    const start = toMinutes(window.start)
    const end = toMinutes(window.end)
    if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start || start % 5 !== 0 || end % 5 !== 0 || end > 1440) {
      return "Each window needs a start and end, in 5-minute steps."
    }
    if (spans.some((span) => span.weekday === window.weekday && start < span.end && end > span.start)) {
      return "Hours on the same day can't overlap."
    }
    spans.push({ weekday: window.weekday, start, end })
    if (Number.isFinite(duration) && end - start >= duration) fits = true
  }
  if (Number.isFinite(duration) && duration >= 5 && duration <= 480 && !fits) return "The meeting has to fit inside at least one set of hours."
  return ""
}

function defaultWindows() {
  return [1, 2, 3, 4, 5].map((weekday) => ({ weekday, start: "09:00", end: "17:00" }))
}

export function BookingDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const page = useQuery({
    queryKey: ["booking-page"],
    queryFn: () => api<BookingPage | null>("/calendar/booking"),
    enabled: open,
  })
  const calendars = useQuery({ ...calendarsQuery, enabled: open })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="grid-cols-[minmax(0,1fr)] sm:max-w-[800px]">
        <DialogHeader>
          <DialogTitle>Booking page</DialogTitle>
          <DialogDescription>
            Share a link so people can book a time when you are free. Aether creates the event and emails an invitation.
          </DialogDescription>
        </DialogHeader>
        <QueryView query={page} skeleton={<RowsSkeleton rows={6} />}>
          {(data) => <BookingEditor page={data} calendars={(calendars.data ?? []).filter((calendar) => calendar.access !== "read" && !calendar.subscribed)} />}
        </QueryView>
      </DialogContent>
    </Dialog>
  )
}

function BookingEditor({ page, calendars }: { page: BookingPage | null; calendars: CalendarInfo[] }) {
  const session = useSession()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<BookingType | null | undefined>(undefined)
  const [removing, setRemoving] = useState<BookingType | null>(null)
  const remove = useMutation({
    mutationFn: (id: string) => api(`/calendar/booking/types/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["booking-page"] })
      toast.success("Meeting removed")
      setRemoving(null)
      setEditing(undefined)
    },
  })
  if (!page && session.isPending) return <RowsSkeleton rows={6} />
  return (
    <div className="flex flex-col gap-8">
      <PageForm key={page ? "ready" : "new"} page={page} session={session.data} />
      {page && (
        <section className="flex flex-col gap-3 border-t border-border pt-6">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 className="text-h3">Meeting types</h2>
            <Button type="button" variant="secondary" onClick={() => setEditing(null)} disabled={page.types.length >= 20 || calendars.length === 0}>
              <Plus aria-hidden />
              Add meeting
            </Button>
          </div>
          {page.types.length === 0 ? (
            <p className="text-fg-secondary">Add a meeting, such as a 30-minute call, and the hours you can take it. Hours use {page.timezone.replaceAll("_", " ")}.</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {page.types.map((type) => (
                <li key={type.id} className="flex flex-col gap-3 rounded-xl border border-border px-4 py-3 sm:flex-row sm:items-center">
                  <div className="min-w-0 flex-1">
                    <p className="font-semibold">
                      {type.name}
                      {!type.active && <span className="ml-2 text-sm font-medium text-fg-tertiary">Hidden</span>}
                    </p>
                    <p className="text-sm text-fg-tertiary">
                      {durationLabel(type.durationMinutes)} · {hoursSummary(type.windows)}
                      {type.location ? ` · ${type.location}` : ""}
                      {type.videoUrl ? " · Video link" : ""}
                    </p>
                  </div>
                  <Button type="button" size="sm" variant="secondary" onClick={() => setEditing(type)}>
                    Edit
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}
      {page && editing !== undefined && (
        <TypeDialog
          type={editing}
          calendars={calendars}
          timezone={page.timezone}
          onOpenChange={(next) => !next && setEditing(undefined)}
          onDelete={() => {
            if (!editing) return
            setRemoving(editing)
            setEditing(undefined)
          }}
        />
      )}
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(next) => !next && !remove.isPending && setRemoving(null)}
        title={`Remove “${removing?.name ?? ""}”?`}
        description="The public page stops offering this meeting. Events that were already booked stay on your calendar."
        confirmLabel="Remove meeting"
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </div>
  )
}

function PageForm({ page, session }: { page: BookingPage | null; session?: Session }) {
  const qc = useQueryClient()
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"
  const name = session?.user.displayName || session?.user.email.split("@")[0] || ""
  const form = useForm<PageValues>({
    resolver: zodResolver(pageSchema),
    defaultValues: page
      ? {
          slug: page.slug,
          title: page.title,
          description: page.description,
          timezone: page.timezone,
          minNoticeMinutes: String(page.minNoticeMinutes),
          horizonDays: String(page.horizonDays),
          active: page.active,
        }
      : {
          slug: suggestSlug(name),
          title: name ? `Meet with ${name}` : "Book a meeting",
          description: "",
          timezone: zone,
          minNoticeMinutes: "60",
          horizonDays: "60",
          active: true,
        },
  })
  const save = useMutation({
    mutationFn: (values: PageValues) => api<BookingPage>("/calendar/booking", {
      method: "PUT",
      body: {
        slug: values.slug.trim().toLowerCase(),
        title: values.title.trim(),
        description: values.description.trim(),
        timezone: values.timezone.trim(),
        minNoticeMinutes: Number(values.minNoticeMinutes),
        horizonDays: Number(values.horizonDays),
        active: values.active,
      },
    }),
    onSuccess: (saved) => {
      qc.setQueryData(["booking-page"], saved)
      toast.success(page ? "Booking page saved" : "Booking page created")
    },
  })

  return (
    <form className="flex flex-col gap-4" noValidate onSubmit={form.handleSubmit((values) => save.mutate(values))}>
      {page && (
        <div className="flex flex-col gap-2">
          <CopyField id="booking-url" label="Public link" value={page.url} copyLabel="Copy booking link" />
          {page.active && (
            <a
              href={`/book/${encodeURIComponent(page.slug)}`}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1.5 self-start text-sm font-medium text-link underline underline-offset-3"
            >
              Open public booking page
              <ExternalLink aria-hidden className="size-3.5" />
            </a>
          )}
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field id="booking-title" label="Title" error={form.formState.errors.title?.message}>
          <Input id="booking-title" {...form.register("title")} />
        </Field>
        <Field id="booking-slug" label="Link" hint="Letters, numbers and hyphens. This is the last part of the public address." error={form.formState.errors.slug?.message}>
          <Input id="booking-slug" autoCapitalize="none" spellCheck={false} {...form.register("slug")} />
        </Field>
      </div>
      <Field id="booking-description" label="Description" hint="Optional. Guests see this above the meeting types." error={form.formState.errors.description?.message}>
        <Textarea id="booking-description" className="min-h-24" {...form.register("description")} />
      </Field>
      <Field id="booking-timezone" label="Time zone" hint="Availability is interpreted in this zone. Guests see times in their own zone." error={form.formState.errors.timezone?.message}>
        <Input id="booking-timezone" list="booking-timezone-zones" autoComplete="off" {...form.register("timezone")} />
        <datalist id="booking-timezone-zones">
          {(typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : ["UTC"]).map((tz) => <option key={tz} value={tz} />)}
        </datalist>
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field id="booking-notice" label="Minimum notice (minutes)" hint="0 lets someone book a time that is about to start." error={form.formState.errors.minNoticeMinutes?.message}>
          <Input id="booking-notice" inputMode="numeric" {...form.register("minNoticeMinutes")} />
        </Field>
        <Field id="booking-horizon" label="How far ahead (days)" error={form.formState.errors.horizonDays?.message}>
          <Input id="booking-horizon" inputMode="numeric" {...form.register("horizonDays")} />
        </Field>
      </div>
      <SwitchField control={form.control} name="active" id="booking-active" label="Page is visible" hint="Turn this off to hide the page without deleting your meeting types." />
      <div className="flex justify-end">
        <Button type="submit" loading={save.isPending}>{page ? "Save page" : "Create page"}</Button>
      </div>
    </form>
  )
}

function TypeDialog({
  type,
  calendars,
  timezone,
  onOpenChange,
  onDelete,
}: {
  type: BookingType | null
  calendars: CalendarInfo[]
  timezone: string
  onOpenChange: (open: boolean) => void
  onDelete: () => void
}) {
  const qc = useQueryClient()
  const form = useForm<TypeValues>({
    resolver: zodResolver(typeSchema),
    defaultValues: type
      ? {
          name: type.name,
          description: type.description,
          durationMinutes: String(type.durationMinutes),
          bufferBeforeMinutes: String(type.bufferBeforeMinutes),
          bufferAfterMinutes: String(type.bufferAfterMinutes),
          location: type.location,
          videoUrl: type.videoUrl,
          calendarId: type.calendarId,
          active: type.active,
          windows: type.windows.map((window) => ({ weekday: window.weekday, start: clock(window.startMinute), end: clock(window.endMinute) })),
        }
      : {
          name: "30-minute meeting",
          description: "",
          durationMinutes: "30",
          bufferBeforeMinutes: "0",
          bufferAfterMinutes: "0",
          location: "",
          videoUrl: "",
          calendarId: calendars.find((calendar) => calendar.id === "personal")?.id ?? calendars[0]?.id ?? "personal",
          active: true,
          windows: defaultWindows(),
        },
  })
  const windows = useFieldArray({ control: form.control, name: "windows" })
  const save = useMutation({
    mutationFn: (values: TypeValues) => {
      const body = {
        name: values.name.trim(),
        slug: type?.slug ?? "",
        description: values.description.trim(),
        durationMinutes: Number(values.durationMinutes),
        bufferBeforeMinutes: Number(values.bufferBeforeMinutes),
        bufferAfterMinutes: Number(values.bufferAfterMinutes),
        location: values.location.trim(),
        videoUrl: values.videoUrl.trim(),
        calendarId: values.calendarId,
        active: values.active,
        windows: values.windows.map((window) => ({ weekday: window.weekday, startMinute: toMinutes(window.start), endMinute: toMinutes(window.end) })),
      }
      return type
        ? api<BookingPage>(`/calendar/booking/types/${type.id}`, { method: "PUT", body })
        : api<BookingPage>("/calendar/booking/types", { method: "POST", body })
    },
    onSuccess: (saved) => {
      qc.setQueryData(["booking-page"], saved)
      toast.success(type ? "Meeting saved" : "Meeting added")
      onOpenChange(false)
    },
  })

  return (
    <FormDialog
      open
      wide
      onOpenChange={onOpenChange}
      title={type ? "Edit meeting" : "New meeting"}
      description={`Guests pick a free slot of this length. Hours are in ${timezone.replaceAll("_", " ")}.`}
      submitLabel={type ? "Save meeting" : "Add meeting"}
      pending={save.isPending}
      onSubmit={form.handleSubmit((values) => save.mutate(values))}
      leading={
        type ? (
          <Button type="button" variant="secondary" onClick={onDelete}>
            <Trash2 aria-hidden />
            Remove
          </Button>
        ) : undefined
      }
    >
      <Field id="meeting-name" label="Name" error={form.formState.errors.name?.message}>
        <Input id="meeting-name" {...form.register("name")} />
      </Field>
      <Field id="meeting-description" label="Description" error={form.formState.errors.description?.message}>
        <Textarea id="meeting-description" className="min-h-24" {...form.register("description")} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field id="meeting-duration" label="Length (minutes)" error={form.formState.errors.durationMinutes?.message}>
          <Input id="meeting-duration" inputMode="numeric" {...form.register("durationMinutes")} />
        </Field>
        <Field id="meeting-before" label="Buffer before" hint="Minutes kept free before it." error={form.formState.errors.bufferBeforeMinutes?.message}>
          <Input id="meeting-before" inputMode="numeric" {...form.register("bufferBeforeMinutes")} />
        </Field>
        <Field id="meeting-after" label="Buffer after" hint="Minutes kept free after it." error={form.formState.errors.bufferAfterMinutes?.message}>
          <Input id="meeting-after" inputMode="numeric" {...form.register("bufferAfterMinutes")} />
        </Field>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field id="meeting-location" label="Place" hint="Optional. A room, an address, or a short note." error={form.formState.errors.location?.message}>
          <Input id="meeting-location" {...form.register("location")} />
        </Field>
        <Field id="meeting-video" label="Video link" hint="Optional. Guests see it only after they book." error={form.formState.errors.videoUrl?.message}>
          <Input id="meeting-video" type="url" placeholder="https://" {...form.register("videoUrl")} />
        </Field>
      </div>
      {calendars.length > 0 && (
        <Controller
          control={form.control}
          name="calendarId"
          render={({ field }) => (
            <Field id="meeting-calendar" label="Calendar" hint="The booked event is added here." error={form.formState.errors.calendarId?.message}>
              <Select value={field.value} onValueChange={field.onChange}>
                <SelectTrigger id="meeting-calendar" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {calendars.map((calendar) => (
                    <SelectItem key={calendar.id} value={calendar.id}>{calendar.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
        />
      )}
      <fieldset className="flex flex-col gap-3">
        <legend className="text-label text-fg-secondary">Hours you can meet</legend>
        {windows.fields.map((field, index) => (
          <div key={field.id} className="grid grid-cols-1 items-center gap-2 sm:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(0,1fr)_auto]">
            <Controller
              control={form.control}
              name={`windows.${index}.weekday`}
              render={({ field: dayField }) => (
                <Select value={String(dayField.value)} onValueChange={(val) => dayField.onChange(Number(val))}>
                  <SelectTrigger aria-label="Day" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {weekdays.map((weekday, value) => (
                      <SelectItem key={weekday} value={String(value)}>{weekday}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
            <Input type="time" step={300} aria-label="Start" {...form.register(`windows.${index}.start`)} />
            <Input type="time" step={300} aria-label="End" {...form.register(`windows.${index}.end`)} />
            <IconButton label="Remove these hours" size="icon-sm" onClick={() => windows.remove(index)} disabled={windows.fields.length === 1}>
              <Trash2 />
            </IconButton>
          </div>
        ))}
        {form.formState.errors.windows?.message && <p role="alert" className="text-sm text-danger">{form.formState.errors.windows.message}</p>}
        <Button type="button" variant="secondary" className="self-start" onClick={() => windows.append({ weekday: 1, start: "09:00", end: "17:00" })} disabled={windows.fields.length >= 21}>
          <Plus aria-hidden />
          Add hours
        </Button>
      </fieldset>
      <SwitchField control={form.control} name="active" id="meeting-active" label="Visible on the booking page" />
    </FormDialog>
  )
}

"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Trash2 } from "lucide-react"
import { useState } from "react"
import { type Control, Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"
import { cn } from "cn"

import { RoomField, type RoomRange } from "@/components/calendar/room-finder"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { RecipientInput } from "@/components/recipient-input"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { DialogClose } from "@/components/ui/dialog"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { attendeeStatusLabels } from "@/lib/format"
import { addDays, dateValue, eventRange, fromValues, timeValue, toRFC3339 } from "@/lib/calendar"
import { bookingStatus } from "@/lib/resources"
import type { Attendee, BookingOutcome, CalendarEvent, CalendarEventInput, CalendarInfo, ResourceChoice } from "@/lib/types"

export interface EventSelection {
  key: number
  event?: CalendarEvent
  start: Date
  end: Date
  allDay: boolean
}

const repeats = { none: "Does not repeat", daily: "Daily", weekly: "Weekly", monthly: "Monthly", yearly: "Yearly" }

const reminderChoices: [string, string][] = [
  ["off", "No reminder"],
  ["0", "At the time of the event"],
  ["5", "5 minutes before"],
  ["15", "15 minutes before"],
  ["30", "30 minutes before"],
  ["60", "1 hour before"],
  ["1440", "1 day before"],
]

function range(v: { allDay: boolean; startDate: string; startTime: string; endDate: string; endTime: string }) {
  if (v.allDay) return { start: fromValues(v.startDate), end: addDays(fromValues(v.endDate), 1) }
  return { start: fromValues(v.startDate, v.startTime), end: fromValues(v.endDate, v.endTime) }
}

const schema = z
  .object({
    title: z.string().trim().min(1, "Give the event a title.").max(300, "Use 300 characters or fewer."),
    allDay: z.boolean(),
    startDate: z.string().min(1, "Choose a start date."),
    startTime: z.string(),
    endDate: z.string().min(1, "Choose an end date."),
    endTime: z.string(),
    repeat: z.enum(["none", "daily", "weekly", "monthly", "yearly", "custom"]),
    location: z.string().trim().max(500, "Use 500 characters or fewer."),
    description: z.string().max(10000, "Use 10,000 characters or fewer."),
    attendees: z.array(z.object({ name: z.string().optional(), address: z.string(), kind: z.enum(["room", "equipment"]).optional() })).max(100, "Invite 100 people or fewer."),
    calendarId: z.string(),
    reminder: z.string().refine((v) => v === "off" || (/^\d+$/.test(v) && Number(v) <= 7 * 24 * 60), "Choose a reminder up to 7 days before the event."),
  })
  .refine((v) => v.allDay || (v.startTime && v.endTime), { message: "Choose a start and end time.", path: ["endDate"] })
  .refine((v) => v.allDay || !v.startTime || !v.endTime || range(v).end > range(v).start, { message: "The event must end after it starts.", path: ["endDate"] })
  .refine((v) => !v.allDay || range(v).end > range(v).start, { message: "The last day can’t be before the first day.", path: ["endDate"] })

type Values = z.infer<typeof schema>

function toValues({ event, start, end, allDay }: EventSelection): Values {
  return {
    title: event?.title ?? "",
    allDay,
    startDate: dateValue(start),
    startTime: allDay ? "09:00" : timeValue(start),
    endDate: dateValue(allDay ? addDays(end, -1) : end),
    endTime: allDay ? "10:00" : timeValue(end),
    repeat: event?.repeat || "none",
    location: event?.location ?? "",
    description: event?.description ?? "",
    attendees: event?.attendees.map(({ name, address, kind }) => ({ name, address, kind })) ?? [],
    calendarId: event?.calendarId ?? "personal",
    reminder: event?.reminderMinutes == null ? "off" : String(event.reminderMinutes),
  }
}

function useDebouncedRange(control: Control<Values>, target: EventSelection): RoomRange | null {
  const v = useWatch({ control })
  const values = { allDay: !!v.allDay, startDate: v.startDate ?? "", startTime: v.startTime ?? "", endDate: v.endDate ?? "", endTime: v.endTime ?? "" }
  const valid = !!values.startDate && !!values.endDate && (values.allDay || (!!values.startTime && !!values.endTime))
  const { start, end } = valid ? range(values) : target
  const key = useDebouncedValue(end > start ? `${toRFC3339(start)}|${toRFC3339(end)}` : "", 400)
  if (!key) return null
  const [from, to] = key.split("|")
  return { start: from, end: to }
}

function seriesShift(target: EventSelection, start: Date, end: Date, allDay: boolean) {
  const { event } = target
  if (!event?.repeat) return { start, end }
  const first = eventRange({ ...event, start: event.seriesStart, end: event.seriesStart })
  const offset = first.start.getTime() - target.start.getTime()
  if (allDay) {
    const days = Math.round(offset / 86_400_000)
    return { start: addDays(start, days), end: addDays(end, days) }
  }
  return { start: new Date(start.getTime() + offset), end: new Date(end.getTime() + offset) }
}

export function EventDialog({
  target,
  calendars,
  open,
  onOpenChange,
}: {
  target: EventSelection
  calendars: CalendarInfo[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const qc = useQueryClient()
  const { event } = target
  const viewOnly = !!event?.readOnly
  const readOnly = event?.repeat === "custom" || viewOnly
  const source = calendars.find((c) => c.id === event?.calendarId)
  const writable = calendars.filter((c) => c.access !== "read" && !c.subscribed)
  const calendarQuery = { calendar: event?.calendarId }
  const session = useSession()
  const invited = !!event?.organizer && event.organizer !== session.data?.user.email.toLowerCase()
  const [deleting, setDeleting] = useState(false)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: toValues(target) })
  const allDay = useWatch({ control: form.control, name: "allDay" })
  const errors = form.formState.errors
  const roomRange = useDebouncedRange(form.control, target)
  const choices = useQuery({
    queryKey: ["resources", roomRange?.start, roomRange?.end, event?.id],
    queryFn: () => api<ResourceChoice[]>("/calendar/resources", { query: { start: roomRange?.start, end: roomRange?.end, exclude: event?.id } }),
    placeholderData: keepPreviousData,
    enabled: !invited && !readOnly,
  })
  const roomAddresses = new Set(choices.data?.map((c) => c.address))
  const isRoom = (a: { address: string; kind?: string }) => !!a.kind || roomAddresses.has(a.address.toLowerCase())
  const unchanged = !!event && roomRange?.start === toRFC3339(target.start) && roomRange?.end === toRFC3339(target.end)
  const savedStatus = (address: string) => (unchanged ? event.attendees.find((a) => a.address === address.toLowerCase())?.status : undefined)

  const save = useMutation({
    mutationFn: async (v: Values) => {
      const repeat = v.repeat === "none" || v.repeat === "custom" ? "" : v.repeat
      const picked = range(v)
      const { start, end } = repeat ? seriesShift(target, picked.start, picked.end, v.allDay) : picked
      const body: CalendarEventInput = {
        title: v.title,
        start: toRFC3339(start),
        end: toRFC3339(end),
        allDay: v.allDay,
        location: v.location,
        description: v.description,
        repeat,
        attendees: v.attendees.map((a) => a.address),
        calendarId: v.calendarId,
        reminderMinutes: v.reminder === "off" ? null : Number(v.reminder),
      }
      type Saved = CalendarEvent & { bookings?: BookingOutcome[] }
      return event
        ? api<Saved>(`/calendar/events/${encodeURIComponent(event.id)}`, { method: "PUT", body })
        : api<Saved>("/calendar/events", { method: "POST", body })
    },
    onSuccess: (saved, v) => {
      const bookings = saved.bookings ?? []
      const names = (status: BookingOutcome["status"]) => bookings.filter((b) => b.status === status).map((b) => b.name)
      const description = [
        !invited && v.attendees.some((a) => !isRoom(a)) && "Invitations were sent to the attendees.",
        names("accepted").length > 0 && `Booked ${names("accepted").join(", ")}.`,
        names("tentative").length > 0 && `${names("tentative").join(", ")} ${names("tentative").length === 1 ? "is" : "are"} waiting for approval.`,
      ].filter(Boolean)
      toast.success(event ? "Event saved" : "Event added", { description: description.length ? description.join(" ") : undefined })
      for (const b of bookings.filter((b) => b.status === "declined")) toast.error(`${b.name} wasn’t booked`, { description: b.reason, duration: 10_000 })
      onOpenChange(false)
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
      qc.invalidateQueries({ queryKey: ["resources"] })
    },
  })
  const remove = useMutation({
    mutationFn: () => api(`/calendar/events/${encodeURIComponent(event?.id ?? "")}`, { method: "DELETE", query: calendarQuery }),
    onSuccess: () => {
      toast.success("Event deleted")
      setDeleting(false)
      onOpenChange(false)
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
    },
  })

  const deleteButton = event && !viewOnly && (
    <Button type="button" variant="secondary" onClick={() => setDeleting(true)}>
      <Trash2 aria-hidden />
      Delete
    </Button>
  )

  return (
    <>
      <FormDialog
        open={open}
        onOpenChange={onOpenChange}
        title={readOnly ? event.title : event ? "Edit event" : "New event"}
        description={
          viewOnly
            ? source?.subscribed
              ? "This calendar is subscribed from a link, so its events are updated from that link."
              : "This calendar is shared with you to view only."
            : readOnly
            ? "Custom repeat (edit in the app that created it). Aether can’t save this repeat pattern, so the event is read-only here."
            : event?.repeat
              ? "Changes apply to every occurrence of this repeating event."
              : undefined
        }
        submitLabel={event ? "Save event" : "Add event"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
        leading={deleteButton}
        footer={
          readOnly ? (
            <DialogClose asChild>
              <Button type="button" variant="secondary">
                Close
              </Button>
            </DialogClose>
          ) : undefined
        }
      >
        <fieldset disabled={readOnly} className="contents">
          {!event && writable.length > 1 && (
            <Controller
              control={form.control}
              name="calendarId"
              render={({ field }) => (
                <Field id="event-calendar" label="Calendar">
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="event-calendar" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {writable.map((c) => (
                        <SelectItem key={c.id} value={c.id}>
                          {c.owner ? `${c.name} (${c.owner.name || c.owner.email})` : c.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
          )}
          <Field id="event-title" label="Title" error={errors.title?.message}>
            <Input id="event-title" autoComplete="off" aria-invalid={!!errors.title} {...form.register("title")} />
          </Field>
          <SwitchField control={form.control} name="allDay" id="event-all-day" label="All day" />
          <div className="grid gap-4">
            <Field id="event-start-date" label={allDay ? "First day" : "Starts"}>
              <div className="flex gap-2">
                <Input id="event-start-date" type="date" {...form.register("startDate")} />
                {!allDay && <Input type="time" aria-label="Start time" className="w-32 shrink-0" {...form.register("startTime")} />}
              </div>
            </Field>
            <Field id="event-end-date" label={allDay ? "Last day" : "Ends"} error={errors.endDate?.message}>
              <div className="flex gap-2">
                <Input id="event-end-date" type="date" aria-invalid={!!errors.endDate} aria-describedby={errors.endDate ? "event-end-date-error" : undefined} {...form.register("endDate")} />
                {!allDay && <Input type="time" aria-label="End time" aria-invalid={!!errors.endDate} className="w-32 shrink-0" {...form.register("endTime")} />}
              </div>
            </Field>
          </div>
          <Controller
            control={form.control}
            name="repeat"
            render={({ field }) => (
              <Field id="event-repeat" label="Repeat">
                <Select value={field.value} onValueChange={field.onChange} disabled={readOnly}>
                  <SelectTrigger id="event-repeat" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {Object.entries(repeats).map(([value, label]) => (
                      <SelectItem key={value} value={value}>
                        {label}
                      </SelectItem>
                    ))}
                    {readOnly && <SelectItem value="custom">Custom repeat</SelectItem>}
                  </SelectContent>
                </Select>
              </Field>
            )}
          />
          <Controller
            control={form.control}
            name="reminder"
            render={({ field }) => {
              const choices = reminderChoices.some(([value]) => value === field.value) || field.value === "off"
                ? reminderChoices
                : [...reminderChoices, [field.value, `${field.value} minutes before`] as [string, string]]
              return (
                <Field id="event-reminder" label="Reminder" hint={allDay ? "For an all-day event, this is at the start of that day." : undefined}>
                  <Select value={field.value} onValueChange={field.onChange} disabled={readOnly}>
                    <SelectTrigger id="event-reminder" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {choices.map(([value, label]) => (
                        <SelectItem key={value} value={value}>
                          {label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )
            }}
          />
          {invited ? (
            <Field id="event-attendees" label="Attendees">
              <AttendeeList organizer={event.organizer} attendees={event.attendees} />
            </Field>
          ) : (
            <Controller
              control={form.control}
              name="attendees"
              render={({ field }) => {
                const people = field.value.filter((a) => !isRoom(a))
                const rooms = field.value.filter(isRoom)
                const saved = event?.attendees.filter((a) => !isRoom(a)) ?? []
                return (
                  <>
                    <Field id="event-attendees" label="Attendees" error={errors.attendees?.message}>
                      <RecipientInput id="event-attendees" value={people} onChange={(next) => field.onChange([...next, ...rooms])} invalid={!!errors.attendees} placeholder="Add people by email" suggest />
                      <Availability control={form.control} target={target} isRoom={isRoom} />
                      {saved.length > 0 && <AttendeeList attendees={saved} />}
                    </Field>
                    <RoomField rooms={rooms} choices={choices} range={roomRange} savedStatus={savedStatus} onChange={(next) => field.onChange([...people, ...next])} />
                  </>
                )
              }}
            />
          )}
          <Field id="event-location" label="Location" error={errors.location?.message}>
            <Input id="event-location" autoComplete="off" aria-invalid={!!errors.location} {...form.register("location")} />
          </Field>
          <Field id="event-description" label="Description" error={errors.description?.message}>
            <Textarea id="event-description" rows={4} aria-invalid={!!errors.description} {...form.register("description")} />
          </Field>
        </fieldset>
      </FormDialog>

      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete “${event?.title ?? ""}”?`}
        description={[
          event?.repeat
            ? "This deletes every occurrence of this repeating event, here and in calendar apps synced to Aether."
            : "The event is also removed from calendar apps synced to Aether. This cannot be undone.",
          invited ? "The organizer is told you’re not going." : event?.attendees.length ? "Attendees are told the event is cancelled." : "",
        ].join(" ")}
        confirmLabel="Delete event"
        pending={remove.isPending}
        onConfirm={() => remove.mutate()}
      />
    </>
  )
}

function AttendeeList({ organizer, attendees }: { organizer?: string; attendees: Attendee[] }) {
  return (
    <ul className="flex flex-col gap-1 text-sm">
      {organizer && (
        <li className="flex justify-between gap-4">
          <span className="truncate">{organizer}</span>
          <span className="shrink-0 text-fg-tertiary">Organizer</span>
        </li>
      )}
      {attendees.map((a) => (
        <li key={a.address} className="flex justify-between gap-4">
          <span className="truncate">{a.name || a.address}</span>
          <span className="shrink-0 text-fg-tertiary">{a.kind ? bookingStatus[a.status].label : attendeeStatusLabels[a.status]}</span>
        </li>
      ))}
    </ul>
  )
}

function Availability({ control, target, isRoom }: { control: Control<Values>; target: EventSelection; isRoom: (a: { address: string; kind?: string }) => boolean }) {
  const v = useWatch({ control })
  const values = { allDay: !!v.allDay, startDate: v.startDate ?? "", startTime: v.startTime ?? "", endDate: v.endDate ?? "", endTime: v.endTime ?? "" }
  const valid = !values.allDay && !!values.startDate && !!values.startTime && !!values.endDate && !!values.endTime
  const { start, end } = valid ? range(values) : { start: target.start, end: target.end }
  const unchanged = !!target.event && start.getTime() === target.start.getTime() && end.getTime() === target.end.getTime()
  const attendees = (v.attendees ?? []).filter((a) => a?.address && !isRoom({ address: a.address, kind: a.kind })).map((a) => a?.address ?? "")
  const debounced = useDebouncedValue(valid && !unchanged && attendees.length > 0 && end > start ? JSON.stringify({ start: toRFC3339(start), end: toRFC3339(end), attendees }) : "", 400)
  const key: { start: string; end: string; attendees: string[] } | null = debounced ? JSON.parse(debounced) : null
  const freeBusy = useQuery({
    queryKey: ["freebusy", key],
    queryFn: () =>
      api<{ email: string; known: boolean; busy: { start: string; end: string }[] }[]>(
        `/calendar/freebusy?${new URLSearchParams([["start", key!.start], ["end", key!.end], ...key!.attendees.map((a) => ["attendee", a])])}`
      ),
    enabled: !!key,
  })
  if (!key || !freeBusy.data) return null
  const known = freeBusy.data.filter((f) => f.known)
  const busy = known.filter((f) => f.busy.length > 0).map((f) => (v.attendees ?? []).find((a) => a?.address?.toLowerCase() === f.email)?.name || f.email)
  if (known.length === 0) return null
  return (
    <p role="status" className={cn("text-sm", busy.length ? "text-warning" : "text-fg-tertiary")}>
      {busy.length ? `Busy at this time: ${busy.join(", ")}` : known.length === 1 ? "Free at this time." : "Everyone from your organization is free at this time."}
    </p>
  )
}

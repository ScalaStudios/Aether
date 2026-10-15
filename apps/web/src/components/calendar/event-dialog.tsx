"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { RecipientInput } from "@/components/recipient-input"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { DialogClose } from "@/components/ui/dialog"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { attendeeStatusLabels } from "@/lib/format"
import { addDays, dateValue, eventRange, fromValues, timeValue, toRFC3339 } from "@/lib/calendar"
import type { Attendee, CalendarEvent, CalendarEventInput } from "@/lib/types"

export interface EventSelection {
  key: number
  event?: CalendarEvent
  start: Date
  end: Date
  allDay: boolean
}

const repeats = { none: "Does not repeat", daily: "Daily", weekly: "Weekly", monthly: "Monthly", yearly: "Yearly" }

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
    attendees: z.array(z.object({ name: z.string().optional(), address: z.string() })).max(100, "Invite 100 people or fewer."),
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
    attendees: event?.attendees.map(({ name, address }) => ({ name, address })) ?? [],
  }
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

export function EventDialog({ target, open, onOpenChange }: { target: EventSelection; open: boolean; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient()
  const { event } = target
  const readOnly = event?.repeat === "custom"
  const session = useSession()
  const invited = !!event?.organizer && event.organizer !== session.data?.user.email.toLowerCase()
  const [deleting, setDeleting] = useState(false)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: toValues(target) })
  const allDay = useWatch({ control: form.control, name: "allDay" })
  const errors = form.formState.errors

  const save = useMutation({
    mutationFn: async (v: Values) => {
      const repeat = v.repeat === "none" || v.repeat === "custom" ? "" : v.repeat
      const picked = range(v)
      const { start, end } = repeat ? seriesShift(target, picked.start, picked.end, v.allDay) : picked
      const body: CalendarEventInput = { title: v.title, start: toRFC3339(start), end: toRFC3339(end), allDay: v.allDay, location: v.location, description: v.description, repeat, attendees: v.attendees.map((a) => a.address) }
      return event
        ? api<CalendarEvent>(`/calendar/events/${encodeURIComponent(event.id)}`, { method: "PUT", body })
        : api<CalendarEvent>("/calendar/events", { method: "POST", body })
    },
    onSuccess: (_, v) => {
      toast.success(event ? "Event saved" : "Event added", { description: !invited && v.attendees.length > 0 ? "Invitations were sent to the attendees." : undefined })
      onOpenChange(false)
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
    },
  })
  const remove = useMutation({
    mutationFn: () => api(`/calendar/events/${encodeURIComponent(event?.id ?? "")}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Event deleted")
      setDeleting(false)
      onOpenChange(false)
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
    },
  })

  const deleteButton = event && (
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
          readOnly
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
          {invited ? (
            <Field id="event-attendees" label="Attendees">
              <AttendeeList organizer={event.organizer} attendees={event.attendees} />
            </Field>
          ) : (
            <Controller
              control={form.control}
              name="attendees"
              render={({ field }) => (
                <Field id="event-attendees" label="Attendees" error={errors.attendees?.message}>
                  <RecipientInput id="event-attendees" value={field.value} onChange={field.onChange} invalid={!!errors.attendees} placeholder="Add people by email" suggest />
                  {event && event.attendees.length > 0 && <AttendeeList attendees={event.attendees} />}
                </Field>
              )}
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
          <span className="shrink-0 text-fg-tertiary">{attendeeStatusLabels[a.status]}</span>
        </li>
      ))}
    </ul>
  )
}

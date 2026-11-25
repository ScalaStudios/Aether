"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { DoorOpen, Pencil, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { DomainSelect } from "@/components/admin/domain-select"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Input, Textarea } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, ApiError, errorMessage } from "@/lib/api"
import { membersQuery } from "@/lib/queries"
import { clock, durationLabel, fromClock, resourceDetails, resourceTypes, ruleSummary, weekdays } from "@/lib/resources"
import type { AdminResource, Member, Mailbox, ResourceSettings, ResourceType } from "@/lib/types"

const people = z.array(z.object({ name: z.string().optional(), address: z.string() }))

const schema = z
  .object({
    type: z.enum(["room", "equipment"]),
    name: z.string().trim().min(1, "Give it a name people will recognize, like “Board Room”.").max(200, "Use 200 characters or fewer."),
    domainId: z.string().min(1, "Choose a domain."),
    localPart: z
      .string()
      .trim()
      .toLowerCase()
      .regex(/^[a-z0-9]([a-z0-9._+-]*[a-z0-9])?$/, "Use letters, digits, dots, hyphens, plus signs or underscores."),
    capacity: z.string().regex(/^\d*$/, "Enter a whole number of people."),
    location: z.string().trim().max(200, "Use 200 characters or fewer."),
    description: z.string().trim().max(2000, "Use 2,000 characters or fewer."),
    policy: z.enum(["auto", "approval"]),
    approvers: people,
    bookableBy: z.enum(["organization", "selected"]),
    bookers: people,
    windowDays: z.string().regex(/^\d+$/, "Enter a number of days."),
    maxMinutes: z.string(),
    workingHours: z.boolean(),
    workDays: z.array(z.number()).min(1, "Choose at least one day."),
    workStart: z.string().min(1, "Choose when bookings can start."),
    workEnd: z.string().min(1, "Choose when bookings must end."),
    timeZone: z.string().min(1, "Choose a time zone."),
  })
  .refine((v) => v.capacity === "" || (Number(v.capacity) >= 1 && Number(v.capacity) <= 10000), { message: "Enter 1 to 10,000 people, or leave it empty.", path: ["capacity"] })
  .refine((v) => Number(v.windowDays) >= 1 && Number(v.windowDays) <= 1095, { message: "Allow booking from 1 to 1,095 days ahead.", path: ["windowDays"] })
  .refine((v) => v.policy === "auto" || v.approvers.length > 0, { message: "Add at least one approver.", path: ["approvers"] })
  .refine((v) => !v.workingHours || !v.workStart || !v.workEnd || fromClock(v.workEnd) > fromClock(v.workStart), { message: "Bookable hours must end after they start.", path: ["workEnd"] })

type Values = z.infer<typeof schema>

const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone
const timeZones = ["UTC", ...Intl.supportedValuesOf("timeZone").filter((z) => z !== "UTC")]

const typeOptions = (Object.keys(resourceTypes) as ResourceType[]).map((value) => ({ value, label: resourceTypes[value].label }))
const policyOptions = [
  { value: "auto" as const, label: "Accept automatically" },
  { value: "approval" as const, label: "Require approval" },
]
const bookableOptions = [
  { value: "organization" as const, label: "Everyone in the organization" },
  { value: "selected" as const, label: "Selected people" },
]
const durations = [30, 60, 90, 120, 180, 240, 480, 1440, 10080]

const blank: Values = {
  type: "room",
  name: "",
  domainId: "",
  localPart: "",
  capacity: "",
  location: "",
  description: "",
  policy: "auto",
  approvers: [],
  bookableBy: "organization",
  bookers: [],
  windowDays: "180",
  maxMinutes: "none",
  workingHours: false,
  workDays: [1, 2, 3, 4, 5],
  workStart: "08:00",
  workEnd: "18:00",
  timeZone: browserZone,
}

const fieldErrors: Record<string, keyof Values> = {
  invalid_local_part: "localPart",
  address_taken: "localPart",
  address_is_account: "localPart",
  invalid_domain: "domainId",
  invalid_name: "name",
  invalid_capacity: "capacity",
  invalid_location: "location",
  invalid_description: "description",
  invalid_approvers: "approvers",
  invalid_bookers: "bookers",
  invalid_window: "windowDays",
  invalid_duration: "maxMinutes",
  invalid_hours: "workEnd",
  invalid_time_zone: "timeZone",
}

function toValues(r: AdminResource, members: Member[]): Values {
  const person = (id: string) => {
    const m = members.find((p) => p.userId === id)
    return m ? { name: m.displayName || undefined, address: m.email } : { address: id }
  }
  return {
    type: r.type,
    name: r.name,
    domainId: r.domainId,
    localPart: r.localPart,
    capacity: r.capacity ? String(r.capacity) : "",
    location: r.location,
    description: r.description,
    policy: r.policy,
    approvers: r.approvers.map(person),
    bookableBy: r.bookableBy,
    bookers: r.bookers.map(person),
    windowDays: String(r.windowDays),
    maxMinutes: r.maxMinutes ? String(r.maxMinutes) : "none",
    workingHours: r.workingHours,
    workDays: r.workDays,
    workStart: clock(r.workStart),
    workEnd: clock(r.workEnd),
    timeZone: r.timeZone,
  }
}

function userIds(list: Values["approvers"], members: Member[], field: "approvers" | "bookers") {
  const found = list.map((a) => ({ ...a, id: members.find((m) => m.email.toLowerCase() === a.address.toLowerCase())?.userId }))
  const outside = found.filter((a) => !a.id).map((a) => a.address)
  if (outside.length) {
    throw new ApiError(422, `invalid_${field}`, `${outside.join(", ")} ${outside.length === 1 ? "isn’t a member" : "aren’t members"} of this organization. Add them under Members first.`)
  }
  return found.map((a) => a.id as string)
}

export function ResourcesTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const key = ["resources", orgId]
  const resources = useQuery({ queryKey: key, queryFn: () => api<AdminResource[]>(`/organizations/${orgId}/resources`) })
  const [editing, setEditing] = useState<AdminResource | "new" | null>(null)
  const [deleting, setDeleting] = useState<AdminResource | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: blank })
  const [type, policy, bookableBy, workingHours] = useWatch({ control: form.control, name: ["type", "policy", "bookableBy", "workingHours"] })
  const errors = form.formState.errors
  const existing = editing !== "new" ? editing : null

  const refresh = () => {
    qc.invalidateQueries({ queryKey: key })
    qc.invalidateQueries({ queryKey: ["mailboxes", orgId] })
  }

  async function open(target: AdminResource | "new") {
    try {
      form.reset(target === "new" ? blank : toValues(target, await qc.ensureQueryData(membersQuery(orgId))))
      setEditing(target)
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }

  const save = useMutation({
    mutationFn: async (v: Values) => {
      const members = await qc.ensureQueryData(membersQuery(orgId))
      const settings: ResourceSettings = {
        type: v.type,
        capacity: v.type === "room" && v.capacity ? Number(v.capacity) : null,
        location: v.location,
        description: v.description,
        policy: v.policy,
        bookableBy: v.bookableBy,
        windowDays: Number(v.windowDays),
        maxMinutes: v.maxMinutes === "none" ? null : Number(v.maxMinutes),
        workingHours: v.workingHours,
        workDays: v.workDays,
        workStart: fromClock(v.workStart),
        workEnd: fromClock(v.workEnd),
        timeZone: v.timeZone,
        approvers: userIds(v.approvers, members, "approvers"),
        bookers: v.bookableBy === "selected" ? userIds(v.bookers, members, "bookers") : [],
      }
      if (existing) return api(`/mailboxes/${existing.id}/resource`, { method: "PUT", body: { ...settings, displayName: v.name } })
      return api<Mailbox>(`/organizations/${orgId}/mailboxes`, {
        method: "POST",
        body: { kind: "resource", domainId: v.domainId, localPart: v.localPart, displayName: v.name, resource: settings },
      })
    },
    meta: { silent: true },
    onSuccess: (_d, v) => {
      toast.success(existing ? `Saved ${v.name}` : `Added ${v.name}`, { description: existing ? undefined : "People can now find it when they create an event." })
      refresh()
      setEditing(null)
    },
    onError: (error) => {
      const field = error instanceof ApiError ? fieldErrors[error.code] : undefined
      if (field) form.setError(field, { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const toggle = useMutation({
    mutationFn: (r: AdminResource) => api(`/mailboxes/${r.id}`, { method: "PATCH", body: { active: !r.active } }),
    onSuccess: (_d, r) => {
      toast.success(r.active ? `${r.name} can no longer be booked` : `${r.name} can be booked again`)
      refresh()
    },
  })
  const remove = useMutation({
    mutationFn: (r: AdminResource) => api(`/mailboxes/${r.id}`, { method: "DELETE" }),
    onSuccess: (_d, r) => {
      toast.success(`Deleted ${r.name}`)
      setDeleting(null)
      refresh()
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Rooms and equipment"
        description="Meeting rooms, cars, projectors and anything else people book. Invitations to them are answered automatically, so nothing is booked twice."
        actions={
          <Button onClick={() => open("new")}>
            <Plus aria-hidden />
            Add room or equipment
          </Button>
        }
      />
      <QueryView
        query={resources}
        empty={
          <EmptyState
            icon={DoorOpen}
            title="No rooms or equipment yet"
            description="Add a meeting room or a piece of equipment. People in this organization can then book it from Aether Calendar or invite it from any calendar app."
            action={<Button onClick={() => open("new")}>Add room or equipment</Button>}
          />
        }
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Details</TableHead>
                <TableHead>Booking rules</TableHead>
                <TableHead>Bookable</TableHead>
                <TableHead className="w-20">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((r) => {
                const Icon = resourceTypes[r.type].icon
                return (
                  <TableRow key={r.id}>
                    <TableCell>
                      <div className="flex flex-col">
                        <span className="font-semibold">{r.name}</span>
                        <span className="font-mono text-sm text-fg-tertiary">{r.address}</span>
                      </div>
                    </TableCell>
                    <TableCell>
                      <span className="inline-flex items-center gap-2">
                        <Icon aria-hidden className="size-4 text-fg-tertiary" />
                        {resourceTypes[r.type].label}
                      </span>
                    </TableCell>
                    <TableCell className="text-fg-secondary">{resourceDetails(r) || "—"}</TableCell>
                    <TableCell>
                      <div className="flex max-w-80 flex-wrap gap-1">
                        {[...ruleSummary(r), `${r.windowDays} days ahead`].map((rule) => (
                          <StatusBadge key={rule} tone={rule === "Needs approval" ? "info" : "neutral"}>
                            {rule}
                          </StatusBadge>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Switch
                        checked={r.active}
                        disabled={toggle.isPending && toggle.variables?.id === r.id}
                        onCheckedChange={() => toggle.mutate(r)}
                        aria-label={`${r.active ? "Stop bookings for" : "Allow bookings for"} ${r.name}`}
                      />
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        <IconButton label={`Edit ${r.name}`} size="icon-sm" onClick={() => open(r)}>
                          <Pencil />
                        </IconButton>
                        <IconButton label={`Delete ${r.name}`} size="icon-sm" onClick={() => setDeleting(r)}>
                          <Trash2 />
                        </IconButton>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
        title={existing ? `Edit ${existing.name}` : "Add room or equipment"}
        description={existing ? <span className="font-mono">{existing.address}</span> : "People invite it like a person. Aether answers every invitation by these rules."}
        submitLabel={existing ? "Save changes" : "Add"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
        wide
      >
        <Controller
          control={form.control}
          name="type"
          render={({ field }) => (
            <fieldset className="flex flex-col items-start gap-2">
              <legend className="mb-2 text-label text-fg-secondary">Type</legend>
              <SegmentedControl label="Type" options={typeOptions} value={field.value} onChange={field.onChange} />
            </fieldset>
          )}
        />
        <Field id="res-name" label="Name" hint="Shown in the room finder, the directory and invitations." error={errors.name?.message}>
          <Input id="res-name" placeholder={type === "room" ? "Board Room" : "Projector 2"} autoComplete="off" aria-invalid={!!errors.name} {...form.register("name")} />
        </Field>
        {!existing && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="res-local" label="Address" hint="People and calendar apps invite this address." error={errors.localPart?.message}>
              <Input id="res-local" placeholder={type === "room" ? "board-room" : "projector-2"} autoComplete="off" spellCheck={false} aria-invalid={!!errors.localPart} {...form.register("localPart")} />
            </Field>
            <DomainSelect id="res-domain" orgId={orgId} control={form.control} />
          </div>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="res-location" label="Location" hint="Optional, for example “Berlin, 3rd floor”." error={errors.location?.message}>
            <Input id="res-location" autoComplete="off" aria-invalid={!!errors.location} {...form.register("location")} />
          </Field>
          {type === "room" && (
            <Field id="res-capacity" label="Seats" hint="Optional. Helps people pick a room that fits." error={errors.capacity?.message}>
              <Input id="res-capacity" inputMode="numeric" autoComplete="off" aria-invalid={!!errors.capacity} {...form.register("capacity")} />
            </Field>
          )}
        </div>
        <Field id="res-description" label="Description" hint="Optional. Equipment, access or anything people should know." error={errors.description?.message}>
          <Textarea id="res-description" rows={3} className="min-h-24" aria-invalid={!!errors.description} {...form.register("description")} />
        </Field>

        <div className="flex flex-col gap-4 border-t border-border pt-6">
          <h3 className="text-label text-fg-secondary">Booking rules</h3>
          <Controller
            control={form.control}
            name="policy"
            render={({ field }) => (
              <fieldset className="flex flex-col items-start gap-2">
                <legend className="mb-2 text-label text-fg-secondary">Requests</legend>
                <SegmentedControl label="Requests" options={policyOptions} value={field.value} onChange={field.onChange} />
                <p className="text-sm text-fg-tertiary">
                  {field.value === "auto"
                    ? "Accepted when it’s free and the rules below allow it. Conflicts are always declined."
                    : "Free, allowed requests wait for an approver. Approvers’ own bookings are accepted right away."}
                </p>
              </fieldset>
            )}
          />
          {policy === "approval" && (
            <Controller
              control={form.control}
              name="approvers"
              render={({ field, fieldState }) => (
                <Field id="res-approvers" label="Approvers" hint="People in this organization who approve or decline requests. They get an email for each one." error={fieldState.error?.message}>
                  <RecipientInput id="res-approvers" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
                </Field>
              )}
            />
          )}
          <Controller
            control={form.control}
            name="bookableBy"
            render={({ field }) => (
              <fieldset className="flex flex-col items-start gap-2">
                <legend className="mb-2 text-label text-fg-secondary">Who can book</legend>
                <SegmentedControl label="Who can book" options={bookableOptions} value={field.value} onChange={field.onChange} />
              </fieldset>
            )}
          />
          {bookableBy === "selected" && (
            <Controller
              control={form.control}
              name="bookers"
              render={({ field, fieldState }) => (
                <Field id="res-bookers" label="People who can book" hint="Everyone else is declined. Approvers can always book." error={fieldState.error?.message}>
                  <RecipientInput id="res-bookers" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
                </Field>
              )}
            />
          )}
          <div className="grid gap-4 sm:grid-cols-3">
            <Field id="res-window" label="Book up to (days ahead)" hint="Repeating bookings may continue past this." error={errors.windowDays?.message}>
              <Input id="res-window" inputMode="numeric" autoComplete="off" aria-invalid={!!errors.windowDays} {...form.register("windowDays")} />
            </Field>
            <Controller
              control={form.control}
              name="maxMinutes"
              render={({ field, fieldState }) => (
                <Field id="res-duration" label="Longest booking" error={fieldState.error?.message}>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="res-duration" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="none">No limit</SelectItem>
                      {[...new Set([...durations, ...(field.value === "none" ? [] : [Number(field.value)])])]
                        .sort((a, b) => a - b)
                        .map((m) => (
                          <SelectItem key={m} value={String(m)}>
                            {durationLabel(m)}
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
            <Controller
              control={form.control}
              name="timeZone"
              render={({ field, fieldState }) => (
                <Field id="res-zone" label="Time zone" error={fieldState.error?.message}>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="res-zone" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {timeZones.map((z) => (
                        <SelectItem key={z} value={z}>
                          {z.replaceAll("_", " ")}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
          </div>
          <SwitchField control={form.control} name="workingHours" id="res-hours" label="Only during set hours" hint="Bookings must start and end inside these hours, on one of these days." />
          {workingHours && (
            <div className="flex flex-col gap-4 rounded-lg border border-border p-4">
              <Controller
                control={form.control}
                name="workDays"
                render={({ field, fieldState }) => (
                  <fieldset className="flex flex-col items-start gap-2">
                    <legend className="mb-2 text-label text-fg-secondary">Days</legend>
                    <SegmentedControl
                      label="Days"
                      options={weekdays}
                      value={field.value}
                      onChange={(day) => field.onChange(field.value.includes(day) ? field.value.filter((d) => d !== day) : [...field.value, day])}
                    />
                    {fieldState.error && (
                      <p role="alert" className="text-sm text-danger">
                        {fieldState.error.message}
                      </p>
                    )}
                  </fieldset>
                )}
              />
              <div className="grid gap-4 sm:grid-cols-2">
                <Field id="res-start" label="From">
                  <Input id="res-start" type="time" step={900} {...form.register("workStart")} />
                </Field>
                <Field id="res-end" label="Until" error={errors.workEnd?.message}>
                  <Input id="res-end" type="time" step={900} aria-invalid={!!errors.workEnd} {...form.register("workEnd")} />
                </Field>
              </div>
            </div>
          )}
        </div>
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name}?`}
        description="People can no longer book it, and its booking calendar and waiting requests are deleted. Events that already include it keep it in their attendee list. To pause bookings instead, turn off Bookable."
        confirmLabel="Delete"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </section>
  )
}

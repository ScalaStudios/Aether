"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ListTodo, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api } from "@/lib/api"
import { dateValue, fromValues, timeValue, toRFC3339 } from "@/lib/calendar"
import type { CalendarInfo, Task, TaskInput, TaskStatus } from "@/lib/types"
import { cn } from "@/lib/utils"

const statuses: { value: TaskStatus; label: string }[] = [
  { value: "needs-action", label: "To do" },
  { value: "in-process", label: "In progress" },
  { value: "completed", label: "Done" },
  { value: "cancelled", label: "Cancelled" },
]

const priorities = [
  { value: 0, label: "No priority" },
  { value: 1, label: "High" },
  { value: 5, label: "Medium" },
  { value: 9, label: "Low" },
]

function normalizePriority(priority: number) {
  if (priority >= 1 && priority <= 4) return 1
  if (priority === 5) return 5
  if (priority >= 6 && priority <= 9) return 9
  return 0
}

function priorityLabel(priority: number) {
  if (priority >= 1 && priority <= 4) return "High priority"
  if (priority === 5) return "Medium priority"
  if (priority >= 6 && priority <= 9) return "Low priority"
  return ""
}

const schema = z.object({
  title: z.string().trim().min(1, "Give the task a title.").max(300, "Use 300 characters or fewer."),
  notes: z.string().max(10000, "Use 10,000 characters or fewer."),
  dueDate: z.string(),
  dueTime: z.string(),
  status: z.enum(["needs-action", "in-process", "completed", "cancelled"]),
  priority: z.number(),
  calendarId: z.string(),
})

type Values = z.infer<typeof schema>

function dueValue(task?: Task): { dueDate: string; dueTime: string } {
  if (!task?.due) return { dueDate: "", dueTime: "" }
  const due = new Date(task.due)
  return { dueDate: dateValue(due), dueTime: timeValue(due) }
}

function toInput(v: Values): TaskInput {
  return {
    title: v.title,
    notes: v.notes.trim(),
    due: v.dueDate ? toRFC3339(fromValues(v.dueDate, v.dueTime || "00:00")) : null,
    status: v.status,
    priority: v.priority,
    calendarId: v.calendarId,
  }
}

const dueFormat = new Intl.DateTimeFormat(undefined, { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })

export function TasksView({ calendars, now }: { calendars: CalendarInfo[]; now: Date }) {
  const qc = useQueryClient()
  const tasks = useQuery({ queryKey: ["tasks"], queryFn: () => api<Task[]>("/calendar/tasks") })
  const [filter, setFilter] = useState<"open" | "done">("open")
  const [editing, setEditing] = useState<Task | null | undefined>(undefined)
  const writable = calendars.filter((c) => c.access !== "read" && !c.subscribed)
  const byId = new Map(calendars.map((c) => [c.id, c]))

  const toggle = useMutation({
    mutationFn: (task: Task) => {
      const next = task.status === "completed" || task.status === "cancelled" ? "needs-action" : "completed"
      const body: TaskInput = { title: task.title, notes: task.notes, due: task.due, status: next, priority: task.priority, calendarId: task.calendarId }
      return api<Task>(`/calendar/tasks/${encodeURIComponent(task.id)}`, { method: "PUT", body, query: { calendar: task.calendarId } })
    },
    onSuccess: (updated) => {
      qc.invalidateQueries({ queryKey: ["tasks"] })
      toast.success(updated.status === "completed" ? "Task marked done" : "Task reopened")
    },
  })

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 px-4 py-4 md:px-6 md:py-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <SegmentedControl
            label="Tasks"
            options={[
              { value: "open", label: "Open" },
              { value: "done", label: "Done" },
            ]}
            value={filter}
            onChange={setFilter}
          />
          <Button onClick={() => setEditing(null)} disabled={writable.length === 0}>
            <Plus aria-hidden />
            New task
          </Button>
        </div>
        <QueryView
          query={tasks}
          empty={
            <EmptyState
              icon={ListTodo}
              title={filter === "open" ? "Nothing waiting on you" : "No finished tasks"}
              description={filter === "open" ? "Tasks you add here stay in sync with calendar apps over CalDAV." : "Completed and cancelled tasks show up here."}
              action={
                filter === "open" && writable.length > 0 ? (
                  <Button onClick={() => setEditing(null)}>
                    <Plus aria-hidden />
                    New task
                  </Button>
                ) : undefined
              }
            />
          }
          isEmpty={(data) => data.filter((task) => (filter === "done" ? task.status === "completed" || task.status === "cancelled" : task.status === "needs-action" || task.status === "in-process")).length === 0}
        >
          {(data) => (
            <ul className="flex flex-col gap-2">
              {data
                .filter((task) => (filter === "done" ? task.status === "completed" || task.status === "cancelled" : task.status === "needs-action" || task.status === "in-process"))
                .map((task) => {
                  const overdue = task.due && task.status !== "completed" && task.status !== "cancelled" && new Date(task.due).getTime() < now.getTime()
                  const cal = byId.get(task.calendarId)
                  const priority = priorityLabel(task.priority)
                  return (
                    <li key={`${task.calendarId}:${task.id}`} className="flex items-start gap-3 rounded-xl border border-border bg-card px-3 py-3 transition-colors duration-160 hover:border-strong">
                      <input
                        type="checkbox"
                        className="mt-1 size-5 shrink-0 cursor-pointer accent-primary"
                        checked={task.status === "completed"}
                        disabled={task.readOnly || toggle.isPending}
                        aria-label={task.status === "completed" ? `Mark “${task.title}” not done` : `Mark “${task.title}” done`}
                        onChange={() => toggle.mutate(task)}
                      />
                      <button type="button" onClick={() => setEditing(task)} className="flex min-w-0 flex-1 cursor-pointer flex-col gap-1 text-left">
                        <span className={cn("font-semibold", task.status === "completed" && "text-fg-tertiary line-through")}>{task.title}</span>
                        <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-fg-tertiary">
                          {task.due && <span className={cn(overdue && "font-medium text-danger")}>{overdue ? "Overdue · " : ""}{dueFormat.format(new Date(task.due))}</span>}
                          <span className="inline-flex items-center gap-1.5">
                            <span aria-hidden className="size-2 rounded-full" style={{ background: cal?.color || "var(--color-ember)" }} />
                            {cal?.name ?? "Calendar"}
                          </span>
                          {priority && <span className={cn(task.priority >= 1 && task.priority <= 4 && "font-medium text-warning")}>{priority}</span>}
                          {task.status === "in-process" && <span>In progress</span>}
                          {task.status === "cancelled" && <span>Cancelled</span>}
                          {task.readOnly && <span>View only</span>}
                        </span>
                      </button>
                    </li>
                  )
                })}
            </ul>
          )}
        </QueryView>
      </div>
      {editing !== undefined && (
        <TaskDialog
          task={editing}
          calendars={writable}
          onOpenChange={(open) => !open && setEditing(undefined)}
        />
      )}
    </div>
  )
}

function TaskDialog({ task, calendars, onOpenChange }: { task: Task | null; calendars: CalendarInfo[]; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient()
  const due = dueValue(task ?? undefined)
  const [deleting, setDeleting] = useState(false)
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      title: task?.title ?? "",
      notes: task?.notes ?? "",
      dueDate: due.dueDate,
      dueTime: due.dueTime || "09:00",
      status: task?.status ?? "needs-action",
      priority: normalizePriority(task?.priority ?? 0),
      calendarId: task?.calendarId ?? calendars.find((c) => c.id === "personal")?.id ?? calendars[0]?.id ?? "personal",
    },
  })
  const dueDate = useWatch({ control: form.control, name: "dueDate" })
  const readOnly = !!task?.readOnly
  const save = useMutation({
    mutationFn: (v: Values) => {
      const body = toInput(v)
      return task
        ? api<Task>(`/calendar/tasks/${encodeURIComponent(task.id)}`, { method: "PUT", body, query: { calendar: task.calendarId } })
        : api<Task>("/calendar/tasks", { method: "POST", body })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["tasks"] })
      toast.success(task ? "Task saved" : "Task added")
      onOpenChange(false)
    },
  })
  const remove = useMutation({
    mutationFn: () => api(`/calendar/tasks/${encodeURIComponent(task?.id ?? "")}`, { method: "DELETE", query: { calendar: task?.calendarId } }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["tasks"] })
      toast.success("Task deleted")
      setDeleting(false)
      onOpenChange(false)
    },
  })

  return (
    <>
      <FormDialog
        open
        onOpenChange={onOpenChange}
        title={readOnly ? task?.title || "Task" : task ? "Edit task" : "New task"}
        description={readOnly ? "This task is in a calendar you can only view, including calendars subscribed from a link." : "Tasks are stored in the calendar you choose and sync to other apps over CalDAV."}
        submitLabel={task ? "Save task" : "Add task"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
        leading={
          task && !readOnly ? (
            <Button type="button" variant="secondary" onClick={() => setDeleting(true)}>
              <Trash2 aria-hidden />
              Delete
            </Button>
          ) : undefined
        }
        footer={
          readOnly ? (
            <Button type="button" variant="secondary" onClick={() => onOpenChange(false)}>
              Close
            </Button>
          ) : undefined
        }
      >
        <fieldset disabled={readOnly} className="contents">
          <Field id="task-title" label="Title" error={form.formState.errors.title?.message}>
            <Input id="task-title" {...form.register("title")} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="task-due-date" label="Due date" hint="Leave empty if it has no deadline.">
              <Input id="task-due-date" type="date" {...form.register("dueDate")} />
            </Field>
            <Field id="task-due-time" label="Due time">
              <Input id="task-due-time" type="time" step={300} disabled={!dueDate || readOnly} {...form.register("dueTime")} />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Controller
              control={form.control}
              name="status"
              render={({ field }) => (
                <Field id="task-status" label="Status">
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="task-status" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {statuses.map((status) => (
                        <SelectItem key={status.value} value={status.value}>
                          {status.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
            <Controller
              control={form.control}
              name="priority"
              render={({ field }) => (
                <Field id="task-priority" label="Priority">
                  <Select value={String(field.value)} onValueChange={(value) => field.onChange(Number(value))}>
                    <SelectTrigger id="task-priority" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {priorities.map((priority) => (
                        <SelectItem key={priority.value} value={String(priority.value)}>
                          {priority.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
          </div>
          {!task && calendars.length > 1 && (
            <Controller
              control={form.control}
              name="calendarId"
              render={({ field }) => (
                <Field id="task-calendar" label="Calendar">
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="task-calendar" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {calendars.map((calendar) => (
                        <SelectItem key={calendar.id} value={calendar.id}>
                          {calendar.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
          )}
          <Field id="task-notes" label="Notes">
            <Textarea id="task-notes" {...form.register("notes")} />
          </Field>
        </fieldset>
      </FormDialog>
      <ConfirmDialog
        open={deleting}
        onOpenChange={(open) => !open && !remove.isPending && setDeleting(false)}
        title={`Delete “${task?.title ?? ""}”?`}
        description="This removes the task from your calendar and any connected CalDAV apps. This cannot be undone."
        confirmLabel="Delete task"
        pending={remove.isPending}
        onConfirm={() => remove.mutate()}
      />
    </>
  )
}

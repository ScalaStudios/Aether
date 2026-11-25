"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, ApiError, errorMessage } from "@/lib/api"
import { plural } from "@/lib/format"
import type { Mailbox, RetentionAction, RetentionPolicy, RetentionRule } from "@/lib/types"

const allMailboxes = "all"
const otherFolder = "other"

const folders = [
  { value: "INBOX", label: "Inbox" },
  { value: "Sent", label: "Sent" },
  { value: "Archive", label: "Archive" },
  { value: "Drafts", label: "Drafts" },
  { value: "Junk", label: "Junk" },
  { value: "Trash", label: "Trash" },
  { value: otherFolder, label: "Another folder" },
]

const actions = [
  { value: "delete" as const, label: "Delete after" },
  { value: "keep" as const, label: "Keep for" },
]

const schema = z
  .object({
    action: z.enum(["delete", "keep"]),
    mailboxId: z.string(),
    folder: z.string(),
    customFolder: z.string().trim(),
    days: z.string().trim().regex(/^\d*$/, "Enter a whole number of days."),
  })
  .refine((v) => v.action === "delete" || v.days !== "", { message: "Enter how many days to keep messages.", path: ["days"] })
  .refine((v) => v.days === "" || (Number(v.days) >= 1 && Number(v.days) <= 36500), { message: "Enter a number of days between 1 and 36,500.", path: ["days"] })
  .refine((v) => v.action === "keep" || v.folder !== otherFolder || v.customFolder !== "", { message: "Enter the folder’s name.", path: ["customFolder"] })

type Values = z.infer<typeof schema>

const blank: Values = { action: "delete", mailboxId: allMailboxes, folder: "INBOX", customFolder: "", days: "" }

const fieldErrors: Record<string, keyof Values> = {
  invalid_folder: "customFolder",
  invalid_days: "days",
  invalid_mailbox: "mailboxId",
}

const folderName = (folder: string) => (folder === "INBOX" ? "Inbox" : folder)

function describe(action: RetentionAction, folder: string, days: number | null) {
  if (action === "keep") return `Keep messages for at least ${plural(days ?? 0, "day")}`
  if (days === null) return `Never delete from ${folderName(folder)}`
  return `Delete from ${folderName(folder)} after ${plural(days, "day")}`
}

type Row = { key: string; policy?: RetentionPolicy; builtIn?: RetentionRule }

export function RetentionTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const policies = useQuery({
    queryKey: ["retention-policies", orgId],
    queryFn: () => api<{ builtIn: RetentionRule[]; policies: RetentionPolicy[] }>(`/organizations/${orgId}/retention-policies`),
  })
  const mailboxes = useQuery({ queryKey: ["mailboxes", orgId], queryFn: () => api<Mailbox[]>(`/organizations/${orgId}/mailboxes`) })
  const [editing, setEditing] = useState<RetentionPolicy | "new" | null>(null)
  const [deleting, setDeleting] = useState<RetentionPolicy | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: blank })
  const [action, folder] = useWatch({ control: form.control, name: ["action", "folder"] })
  const errors = form.formState.errors
  const existing = typeof editing === "object" ? editing : null

  const scope = (p: Pick<RetentionPolicy, "mailboxAddress">) => p.mailboxAddress ?? "All mailboxes"

  function open(target: RetentionPolicy | RetentionRule | "new") {
    if (target === "new") {
      form.reset(blank)
      setEditing("new")
    } else if ("id" in target) {
      form.reset({ ...blank, action: target.action, days: target.days === null ? "" : String(target.days) })
      setEditing(target)
    } else {
      form.reset({ ...blank, folder: target.folder, days: String(target.days) })
      setEditing("new")
    }
  }

  const save = useMutation({
    mutationFn: (v: Values) => {
      const days = v.days === "" ? null : Number(v.days)
      if (existing) return api<RetentionPolicy>(`/retention-policies/${existing.id}`, { method: "PATCH", body: { days } })
      return api<RetentionPolicy>(`/organizations/${orgId}/retention-policies`, {
        method: "POST",
        body: {
          action: v.action,
          mailboxId: v.mailboxId === allMailboxes ? null : v.mailboxId,
          folder: v.action === "keep" ? "" : v.folder === otherFolder ? v.customFolder : v.folder,
          days,
        },
      })
    },
    meta: { silent: true },
    onSuccess: () => {
      toast.success("Policy saved")
      qc.invalidateQueries({ queryKey: ["retention-policies", orgId] })
      qc.invalidateQueries({ queryKey: ["retention"] })
      setEditing(null)
    },
    onError: (error) => {
      const field = error instanceof ApiError ? fieldErrors[error.code] : undefined
      if (field) form.setError(field, { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const remove = useMutation({
    mutationFn: (p: RetentionPolicy) => api(`/retention-policies/${p.id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Policy deleted")
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ["retention-policies", orgId] })
      qc.invalidateQueries({ queryKey: ["retention"] })
    },
  })

  const rows = (data: { builtIn: RetentionRule[]; policies: RetentionPolicy[] }): Row[] => [
    ...data.builtIn
      .filter((b) => !data.policies.some((p) => p.mailboxId === null && p.action === "delete" && p.folder === b.folder))
      .map((b) => ({ key: `built-in-${b.folder}`, builtIn: b })),
    ...data.policies.map((p) => ({ key: p.id, policy: p })),
  ]

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Retention"
        description="Delete messages from a folder once they reach an age, or keep messages for a minimum time. A policy for one mailbox replaces the policy for all mailboxes on the same folder; when several keep policies apply, the longest counts. Policies are applied once a day."
        actions={
          <Button onClick={() => open("new")}>
            <Plus aria-hidden />
            Add policy
          </Button>
        }
      />
      <QueryView query={policies} isEmpty={() => false}>
        {(data) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Applies to</TableHead>
                <TableHead>Policy</TableHead>
                <TableHead className="w-20">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows(data).map(({ key, policy, builtIn }) => {
                const label = policy ? describe(policy.action, policy.folder, policy.days) : describe("delete", builtIn!.folder, builtIn!.days)
                return (
                  <TableRow key={key}>
                    <TableCell className={policy?.mailboxAddress ? "font-mono text-sm" : undefined}>{policy ? scope(policy) : "All mailboxes"}</TableCell>
                    <TableCell>
                      <div className="flex items-center gap-3">
                        <span>{label}</span>
                        {builtIn && <StatusBadge tone="neutral">Default</StatusBadge>}
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        <IconButton label={`Edit policy: ${label}`} size="icon-sm" onClick={() => open(policy ?? builtIn!)}>
                          <Pencil />
                        </IconButton>
                        {policy && (
                          <IconButton label={`Delete policy: ${label}`} size="icon-sm" onClick={() => setDeleting(policy)}>
                            <Trash2 />
                          </IconButton>
                        )}
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
        title={existing ? "Edit policy" : "Add policy"}
        description={existing ? `${scope(existing)}: ${describe(existing.action, existing.folder, existing.days)}` : "Applied to existing and new messages once a day."}
        submitLabel={existing ? "Save policy" : "Add policy"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
      >
        {!existing && (
          <>
            <Controller
              control={form.control}
              name="action"
              render={({ field }) => (
                <fieldset className="flex flex-col items-start gap-2">
                  <legend className="mb-2 text-label text-fg-secondary">Type</legend>
                  <SegmentedControl label="Type" options={actions} value={field.value} onChange={field.onChange} />
                </fieldset>
              )}
            />
            <Controller
              control={form.control}
              name="mailboxId"
              render={({ field, fieldState }) => (
                <Field id="retention-mailbox" label="Applies to" error={fieldState.error?.message}>
                  <Select value={field.value} onValueChange={field.onChange} disabled={mailboxes.isPending}>
                    <SelectTrigger id="retention-mailbox" className="w-full" aria-invalid={!!fieldState.error}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={allMailboxes}>All mailboxes</SelectItem>
                      {mailboxes.data?.map((m) => (
                        <SelectItem key={m.id} value={m.id}>
                          {m.address}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
            {action === "delete" && (
              <Controller
                control={form.control}
                name="folder"
                render={({ field }) => (
                  <Field id="retention-folder" label="Folder" hint="Subfolders are not included. Trash and Junk count from when a message was moved there; other folders from when it arrived.">
                    <Select value={field.value} onValueChange={field.onChange}>
                      <SelectTrigger id="retention-folder" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {folders.map((f) => (
                          <SelectItem key={f.value} value={f.value}>
                            {f.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                )}
              />
            )}
            {action === "delete" && folder === otherFolder && (
              <Field id="retention-custom" label="Folder name" hint="The full name, with / between levels, for example Clients/Acme." error={errors.customFolder?.message}>
                <Input id="retention-custom" autoComplete="off" spellCheck={false} aria-invalid={!!errors.customFolder} {...form.register("customFolder")} />
              </Field>
            )}
          </>
        )}
        <Field
          id="retention-days"
          label={(existing?.action ?? action) === "keep" ? "Keep for at least, in days" : "Delete after, in days"}
          hint={(existing?.action ?? action) === "keep" ? "Retention never deletes messages younger than this. For example, 2557 for seven years." : "Leave empty to never delete, for example to exempt a mailbox from a policy for all mailboxes."}
          error={errors.days?.message}
        >
          <Input id="retention-days" inputMode="numeric" autoComplete="off" className="w-32" aria-invalid={!!errors.days} {...form.register("days")} />
        </Field>
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="Delete policy?"
        description={
          deleting
            ? `${scope(deleting)}: ${describe(deleting.action, deleting.folder, deleting.days)}. ${deleting.mailboxId ? "The mailbox follows the policy for all mailboxes again." : deleting.action === "delete" ? "The folder falls back to the default, if it has one." : "Messages are no longer kept by this policy."}`
            : ""
        }
        confirmLabel="Delete policy"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </section>
  )
}

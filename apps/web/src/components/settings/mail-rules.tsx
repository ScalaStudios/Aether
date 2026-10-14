"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowDown, ArrowUp, ListFilter, Pencil, Plus, Trash2, X } from "lucide-react"
import { useState } from "react"
import { Controller, useFieldArray, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { folderLabel } from "@/lib/mail"
import { foldersQuery, myMailboxesQuery } from "@/lib/queries"
import type { MailRule, RuleField } from "@/lib/types"

const fields: Record<RuleField, string> = { from: "From", to: "To", cc: "Cc", subject: "Subject" }
const keepInPlace = "__keep__"

const schema = z
  .object({
    name: z.string().trim().min(1, "Name the rule.").max(100),
    match: z.enum(["all", "any"]),
    conditions: z
      .array(z.object({ field: z.enum(["from", "to", "cc", "subject"]), contains: z.string().trim().min(1, "Enter text to look for.").max(500) }))
      .min(1, "Add at least one condition."),
    moveTo: z.string(),
    markRead: z.boolean(),
    flag: z.boolean(),
    stop: z.boolean(),
  })
  .refine((v) => v.moveTo !== keepInPlace || v.markRead || v.flag, { message: "Choose at least one action.", path: ["moveTo"] })

type Values = z.infer<typeof schema>

function toValues(rule?: MailRule): Values {
  return {
    name: rule?.name ?? "",
    match: rule?.match ?? "all",
    conditions: rule?.conditions ?? [{ field: "from", contains: "" }],
    moveTo: rule?.actions.moveTo || keepInPlace,
    markRead: rule?.actions.markRead ?? false,
    flag: rule?.actions.flag ?? false,
    stop: rule?.actions.stop ?? true,
  }
}

function toRule(v: Values, enabled: boolean): MailRule {
  return {
    name: v.name,
    enabled,
    match: v.match,
    conditions: v.conditions,
    actions: { moveTo: v.moveTo === keepInPlace ? undefined : v.moveTo, markRead: v.markRead, flag: v.flag, stop: v.stop },
  }
}

function summary(rule: MailRule) {
  const when = rule.conditions.map((c) => `${fields[c.field]} contains “${c.contains}”`).join(rule.match === "all" ? " and " : " or ")
  const then = [rule.actions.moveTo && `move to ${rule.actions.moveTo}`, rule.actions.markRead && "mark as read", rule.actions.flag && "flag"].filter(Boolean).join(", ")
  return { when, then }
}

export function MailRules() {
  const mailboxes = useQuery(myMailboxesQuery)
  const [chosen, setChosen] = useState<string | null>(null)
  const mailboxId = chosen ?? mailboxes.data?.[0]?.id ?? null

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Mail rules"
        description="Rules run on the mail server as messages arrive, so they apply in Aether Mail and in every other mail client. They run in order, after spam filtering."
        actions={
          (mailboxes.data?.length ?? 0) > 1 && (
            <Select value={mailboxId ?? undefined} onValueChange={setChosen}>
              <SelectTrigger aria-label="Mailbox" className="w-64">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {mailboxes.data?.map((m) => (
                  <SelectItem key={m.id} value={m.id}>
                    {m.address}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )
        }
      />
      {mailboxId ? (
        <RuleList key={mailboxId} mailboxId={mailboxId} />
      ) : (
        !mailboxes.isPending && <EmptyState icon={ListFilter} title="No mailbox" description="Rules need a mailbox. Ask an administrator to create one for you." />
      )}
    </section>
  )
}

function RuleList({ mailboxId }: { mailboxId: string }) {
  const qc = useQueryClient()
  const key = ["rules", mailboxId]
  const rules = useQuery({ queryKey: key, queryFn: () => api<MailRule[]>(`/mailboxes/${mailboxId}/rules`) })
  const folders = useQuery(foldersQuery(mailboxId))
  const [editing, setEditing] = useState<number | "new" | null>(null)
  const [deleting, setDeleting] = useState<number | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: toValues() })
  const conditions = useFieldArray({ control: form.control, name: "conditions" })

  const save = useMutation({
    mutationFn: (next: MailRule[]) => api<MailRule[]>(`/mailboxes/${mailboxId}/rules`, { method: "PUT", body: next }),
    onSuccess: (saved) => {
      qc.setQueryData(key, saved)
      setEditing(null)
      setDeleting(null)
      toast.success("Rules saved")
    },
  })

  const list = rules.data ?? []

  function open(index: number | "new") {
    form.reset(toValues(index === "new" ? undefined : list[index]))
    setEditing(index)
  }

  function move(index: number, delta: number) {
    const next = [...list]
    ;[next[index], next[index + delta]] = [next[index + delta], next[index]]
    save.mutate(next)
  }

  const submit = form.handleSubmit((v) => {
    if (editing === null) return
    const next = [...list]
    if (editing === "new") next.push(toRule(v, true))
    else next[editing] = toRule(v, list[editing].enabled)
    save.mutate(next)
  })

  return (
    <>
      <div className="flex justify-end">
        <Button variant="secondary" onClick={() => open("new")}>
          <Plus aria-hidden />
          New rule
        </Button>
      </div>
      <QueryView query={rules} empty={<EmptyState icon={ListFilter} title="No rules yet" description="Create a rule to file, flag, or mark incoming mail automatically." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-16">On</TableHead>
                <TableHead>Rule</TableHead>
                <TableHead className="w-44">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((rule, i) => {
                const { when, then } = summary(rule)
                return (
                  <TableRow key={`${rule.name}-${i}`}>
                    <TableCell>
                      <Switch
                        aria-label={`${rule.enabled ? "Turn off" : "Turn on"} ${rule.name}`}
                        checked={rule.enabled}
                        disabled={save.isPending}
                        onCheckedChange={(enabled) => save.mutate(items.map((r, j) => (j === i ? { ...r, enabled } : r)))}
                      />
                    </TableCell>
                    <TableCell>
                      <p className="font-medium">{rule.name}</p>
                      <p className="text-sm text-fg-tertiary">
                        When {when}, {then}
                        {rule.actions.stop ? ", then stop." : "."}
                      </p>
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <IconButton label={`Move ${rule.name} up`} size="icon-sm" disabled={i === 0 || save.isPending} onClick={() => move(i, -1)}>
                          <ArrowUp />
                        </IconButton>
                        <IconButton label={`Move ${rule.name} down`} size="icon-sm" disabled={i === items.length - 1 || save.isPending} onClick={() => move(i, 1)}>
                          <ArrowDown />
                        </IconButton>
                        <IconButton label={`Edit ${rule.name}`} size="icon-sm" onClick={() => open(i)}>
                          <Pencil />
                        </IconButton>
                        <IconButton label={`Delete ${rule.name}`} size="icon-sm" onClick={() => setDeleting(i)}>
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
        wide
        title={editing === "new" ? "New rule" : "Edit rule"}
        description="Text matching ignores letter case."
        submitLabel="Save rule"
        pending={save.isPending}
        onSubmit={submit}
      >
        <Field id="rule-name" label="Name" error={form.formState.errors.name?.message}>
          <Input id="rule-name" autoComplete="off" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
        </Field>
        <Controller
          control={form.control}
          name="match"
          render={({ field }) => (
            <Field id="rule-match" label="Apply when">
              <Select value={field.value} onValueChange={field.onChange}>
                <SelectTrigger id="rule-match" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All conditions match</SelectItem>
                  <SelectItem value="any">Any condition matches</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          )}
        />
        <fieldset className="flex flex-col gap-3">
          <legend className="mb-2 text-label text-fg-secondary">Conditions</legend>
          {conditions.fields.map((c, i) => (
            <div key={c.id} className="flex items-start gap-2">
              <Controller
                control={form.control}
                name={`conditions.${i}.field`}
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger aria-label={`Condition ${i + 1} field`} className="w-32 shrink-0">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(Object.keys(fields) as RuleField[]).map((f) => (
                        <SelectItem key={f} value={f}>
                          {fields[f]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <Input
                  aria-label={`Condition ${i + 1} contains`}
                  placeholder="contains…"
                  autoComplete="off"
                  aria-invalid={!!form.formState.errors.conditions?.[i]?.contains}
                  {...form.register(`conditions.${i}.contains`)}
                />
                {form.formState.errors.conditions?.[i]?.contains && (
                  <p role="alert" className="text-sm text-danger">{form.formState.errors.conditions[i]?.contains?.message}</p>
                )}
              </div>
              <IconButton type="button" label={`Remove condition ${i + 1}`} disabled={conditions.fields.length === 1} onClick={() => conditions.remove(i)}>
                <X />
              </IconButton>
            </div>
          ))}
          <Button type="button" variant="ghost" className="self-start" disabled={conditions.fields.length >= 20} onClick={() => conditions.append({ field: "subject", contains: "" })}>
            <Plus aria-hidden />
            Add condition
          </Button>
        </fieldset>
        <Controller
          control={form.control}
          name="moveTo"
          render={({ field, fieldState }) => (
            <Field id="rule-move" label="Move to folder" error={fieldState.error?.message}>
              <Select value={field.value} onValueChange={field.onChange}>
                <SelectTrigger id="rule-move" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={keepInPlace}>Don’t move</SelectItem>
                  {folders.data?.map((f) => (
                    <SelectItem key={f.name} value={f.name}>
                      {folderLabel(f)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
        />
        <SwitchField control={form.control} name="markRead" id="rule-read" label="Mark as read" />
        <SwitchField control={form.control} name="flag" id="rule-flag" label="Flag" />
        <SwitchField control={form.control} name="stop" id="rule-stop" label="Stop processing more rules" hint="Later rules are skipped for messages this rule matches." />
      </FormDialog>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete “${deleting !== null ? list[deleting]?.name : ""}”?`}
        description="Messages that arrive from now on are no longer handled by this rule. Messages it already moved stay where they are."
        confirmLabel="Delete rule"
        pending={save.isPending}
        onConfirm={() => deleting !== null && save.mutate(list.filter((_, i) => i !== deleting))}
      />
    </>
  )
}

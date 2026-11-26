"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowDown, ArrowUp, Pencil, Plus, Route, ScrollText, Trash2, X } from "lucide-react"
import { useState } from "react"
import { Controller, useFieldArray, useForm, useWatch, type Control, type FieldErrors, type UseFormRegister } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { LockNote } from "@/components/notice"
import { SectionHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { FlowApprovals } from "@/components/settings/approvals"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import { platformOnly } from "@/lib/permissions"
import type { FlowCondition, FlowField, FlowMatch, FlowRule, FlowScope } from "@/lib/types"

const fields: Record<FlowField, string> = {
  sender: "Sender",
  recipient: "A recipient",
  subject: "Subject contains",
  body: "Body contains",
  header: "Header contains",
  attachmentName: "Attachment name contains",
  attachmentType: "Attachment type contains",
  sizeOver: "Size is over",
  spamScoreOver: "Spam score is over",
  direction: "Direction is",
}

const scopes: Record<FlowScope, string> = {
  address: "Is one of",
  domain: "Is at domain",
  inside: "Is inside the organization",
  outside: "Is outside the organization",
  memberOf: "Is a member of list",
}

const flowTargets = ["bcc", "redirectTo", "approvers"] as const

const directions = { incoming: "Incoming", outgoing: "Outgoing", internal: "Internal" }

const modes = {
  enforce: { label: "Enforce", badge: "On", tone: "success" },
  test: { label: "Test: only record what it would do", badge: "Testing", tone: "info" },
  off: { label: "Off", badge: "Off", tone: "neutral" },
} as const

const numeric = (f: FlowField) => f === "sizeOver" || f === "spamScoreOver"
const addressed = (f: FlowField) => f === "sender" || f === "recipient"
const split = (s: string) => s.split(",").map((v) => v.trim()).filter(Boolean)

const conditionSchema = z
  .object({
    field: z.enum(Object.keys(fields) as [FlowField, ...FlowField[]]),
    scope: z.enum(Object.keys(scopes) as [FlowScope, ...FlowScope[]]),
    header: z.string().trim(),
    values: z.string(),
    number: z.string().trim(),
  })
  .refine((c) => !numeric(c.field) || (c.number !== "" && !Number.isNaN(Number(c.number))), { message: "Enter a number.", path: ["number"] })
  .refine((c) => numeric(c.field) || (addressed(c.field) && (c.scope === "inside" || c.scope === "outside")) || split(c.values).length > 0, { message: "Enter at least one value.", path: ["values"] })
  .refine((c) => c.field !== "header" || /^[A-Za-z0-9-]{1,76}$/.test(c.header), { message: "Enter a header name such as List-Id.", path: ["header"] })

const address = z.object({ name: z.string().optional(), address: z.email() })

const schema = z
  .object({
    name: z.string().trim().min(1, "Name the rule.").max(100),
    mode: z.enum(["enforce", "test", "off"]),
    conditions: z.array(conditionSchema).max(20),
    exceptions: z.array(conditionSchema).max(20),
    reject: z.boolean(),
    rejectMessage: z.string().trim().max(200),
    disclaimer: z.boolean(),
    disclaimerText: z.string().trim().max(10000),
    disclaimerHtml: z.string().trim(),
    fallback: z.enum(["wrap", "skip", "reject"]),
    bcc: z.array(address).max(20, "Up to 20 addresses."),
    redirectTo: z.array(address).max(20, "Up to 20 addresses."),
    approvers: z.array(address).max(20, "Up to 20 approvers."),
    addHeaders: z.array(z.object({ name: z.string().trim().regex(/^[A-Za-z0-9-]{1,76}$/, "Use letters, digits and hyphens."), value: z.string().trim().min(1, "Enter a value.").max(500) })).max(10),
    removeHeaders: z.string(),
    spamScore: z.string().trim().refine((v) => v === "" || (!Number.isNaN(Number(v)) && Math.abs(Number(v)) <= 50 && Number(v) !== 0), "Enter a number between -50 and 50, other than 0."),
    requireTls: z.boolean(),
    stop: z.boolean(),
  })
  .refine((v) => !v.reject || v.rejectMessage !== "", { message: "Write the message the sender gets.", path: ["rejectMessage"] })
  .refine((v) => !v.disclaimer || v.disclaimerText !== "", { message: "Write the disclaimer text.", path: ["disclaimerText"] })

type Values = z.infer<typeof schema>
type ConditionValues = Values["conditions"][number]

const toCondition = (c: FlowCondition): ConditionValues => ({
  field: c.field,
  scope: c.scope ?? "address",
  header: c.header ?? "",
  values: (c.values ?? []).join(", "),
  number: c.number !== undefined && numeric(c.field) ? String(c.number) : "",
})

const fromCondition = (c: ConditionValues): FlowCondition =>
  numeric(c.field)
    ? { field: c.field, number: Number(c.number) }
    : addressed(c.field)
      ? { field: c.field, scope: c.scope, values: c.scope === "inside" || c.scope === "outside" ? undefined : split(c.values) }
      : { field: c.field, header: c.field === "header" ? c.header : undefined, values: c.field === "direction" ? [c.values] : split(c.values) }

const addresses = (list?: string[]) => (list ?? []).map((address) => ({ address }))

function toValues(rule?: FlowRule): Values {
  const a = rule?.actions ?? {}
  return {
    name: rule?.name ?? "",
    mode: rule?.mode ?? "test",
    conditions: (rule?.conditions ?? []).map(toCondition),
    exceptions: (rule?.exceptions ?? []).map(toCondition),
    reject: !!a.reject,
    rejectMessage: a.reject ?? "",
    disclaimer: !!a.disclaimer,
    disclaimerText: a.disclaimer?.text ?? "",
    disclaimerHtml: a.disclaimer?.html ?? "",
    fallback: a.disclaimer?.fallback ?? "wrap",
    bcc: addresses(a.bcc),
    redirectTo: addresses(a.redirectTo),
    approvers: addresses(a.approvers),
    addHeaders: a.addHeaders ?? [],
    removeHeaders: (a.removeHeaders ?? []).join(", "),
    spamScore: a.spamScore !== undefined ? String(a.spamScore) : "",
    requireTls: !!a.requireTls,
    stop: rule?.stop ?? false,
  }
}

function toRule(v: Values) {
  const list = (l: { address: string }[]) => (l.length ? l.map((a) => a.address) : undefined)
  return {
    name: v.name,
    mode: v.mode,
    stop: v.stop,
    conditions: v.conditions.map(fromCondition),
    exceptions: v.exceptions.map(fromCondition),
    actions: v.reject
      ? { reject: v.rejectMessage }
      : {
          disclaimer: v.disclaimer ? { text: v.disclaimerText, html: v.disclaimerHtml || undefined, fallback: v.fallback } : undefined,
          bcc: list(v.bcc),
          redirectTo: list(v.redirectTo),
          approvers: list(v.approvers),
          addHeaders: v.addHeaders.length ? v.addHeaders : undefined,
          removeHeaders: split(v.removeHeaders).length ? split(v.removeHeaders) : undefined,
          spamScore: v.spamScore ? Number(v.spamScore) : undefined,
          requireTls: v.requireTls || undefined,
        },
  }
}

function describe(c: FlowCondition) {
  const values = (c.values ?? []).map((v) => `“${v}”`).join(" or ")
  switch (c.field) {
    case "sender":
    case "recipient":
      return `${c.field === "sender" ? "the sender" : "a recipient"} ${scopes[c.scope ?? "address"].toLowerCase()}${c.scope === "inside" || c.scope === "outside" ? "" : ` ${values}`}`
    case "sizeOver":
      return `the size is over ${c.number} KB`
    case "spamScoreOver":
      return `the spam score is over ${c.number}`
    case "direction":
      return `the message is ${(c.values ?? []).join(" or ")}`
    case "header":
      return `${c.header} contains ${values}`
    default:
      return `${fields[c.field].toLowerCase()} ${values}`
  }
}

function summary(rule: FlowRule) {
  const a = rule.actions
  const when = rule.conditions.length ? rule.conditions.map(describe).join(" and ") : "every message"
  const unless = rule.exceptions.length ? `, unless ${rule.exceptions.map(describe).join(" or ")}` : ""
  const then = [
    a.reject && `reject with “${a.reject}”`,
    a.disclaimer && "add a disclaimer",
    a.bcc?.length && `copy to ${a.bcc.join(", ")}`,
    a.redirectTo?.length && `redirect to ${a.redirectTo.join(", ")}`,
    a.addHeaders?.length && `add ${a.addHeaders.map((h) => h.name).join(", ")}`,
    a.removeHeaders?.length && `remove ${a.removeHeaders.join(", ")}`,
    a.spamScore && `change the spam score by ${a.spamScore}`,
    a.requireTls && "require TLS",
    a.approvers?.length && `hold for approval by ${a.approvers.join(", ")}`,
  ]
    .filter(Boolean)
    .join(", ")
  return `When ${when}${unless}: ${then}${rule.stop ? ", then stop." : "."}`
}

export function MailFlowTab({ orgId }: { orgId: string }) {
  const session = useSession().data
  const locked = platformOnly(session, orgId)
  return (
    <div className="flex flex-col gap-10">
      <RuleList orgId={orgId} lockedFor={locked ? session?.user.email.toLowerCase() : undefined} />
      {locked ? (
        <section className="flex flex-col gap-6">
          <SectionHeader title="Messages held by mail flow rules" />
          <LockNote>Platform administrators need the owner or administrator role in this organization to read and decide on held messages. Ask one of its owners or administrators to grant you the role.</LockNote>
        </section>
      ) : (
        <FlowApprovals queryKey={["held", orgId]} path={`/organizations/${orgId}/mail-flow/held`} />
      )}
      <MatchLog orgId={orgId} />
    </div>
  )
}

function RuleList({ orgId, lockedFor }: { orgId: string; lockedFor?: string }) {
  const qc = useQueryClient()
  const key = ["mail-flow-rules", orgId]
  const base = `/organizations/${orgId}/mail-flow`
  const rules = useQuery({ queryKey: key, queryFn: () => api<FlowRule[]>(`${base}/rules`) })
  const [editing, setEditing] = useState<FlowRule | "new" | null>(null)
  const [deleting, setDeleting] = useState<FlowRule | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: toValues() })
  const [reject, disclaimer] = useWatch({ control: form.control, name: ["reject", "disclaimer"] })
  const headers = useFieldArray({ control: form.control, name: "addHeaders" })
  const errors = form.formState.errors
  const refresh = () => qc.invalidateQueries({ queryKey: key })

  const save = useMutation({
    mutationFn: (v: Values) =>
      editing === "new" || editing === null
        ? api<FlowRule>(`${base}/rules`, { method: "POST", body: toRule(v) })
        : api<FlowRule>(`${base}/rules/${editing.id}`, { method: "PUT", body: toRule(v) }),
    onSuccess: (saved) => {
      toast.success(`Saved “${saved.name}”`)
      setEditing(null)
      refresh()
    },
  })
  const remove = useMutation({
    mutationFn: (rule: FlowRule) => api(`${base}/rules/${rule.id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Rule deleted")
      setDeleting(null)
      refresh()
    },
  })
  const order = useMutation({
    mutationFn: (ids: string[]) => api(`${base}/order`, { method: "PUT", body: { ids } }),
    onSettled: refresh,
  })

  function open(rule: FlowRule | "new") {
    form.reset(toValues(rule === "new" ? undefined : rule))
    setEditing(rule)
  }

  function move(list: FlowRule[], index: number, delta: number) {
    const ids = list.map((r) => r.id)
    ;[ids[index], ids[index + delta]] = [ids[index + delta], ids[index]]
    order.mutate(ids)
  }

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Mail flow rules"
        description="These run on the mail server for every message the organization sends or receives, from any mail app. Rules run in order; rules that test the spam score run after spam filtering, after all others."
        actions={
          <Button variant="secondary" onClick={() => open("new")}>
            <Plus aria-hidden />
            New rule
          </Button>
        }
      />
      <QueryView query={rules} empty={<EmptyState icon={Route} title="No mail flow rules yet" description="Add a disclaimer to outgoing mail, copy messages to a compliance mailbox, reject attachments or hold messages for approval." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-28">Status</TableHead>
                <TableHead>Rule</TableHead>
                <TableHead className="w-44">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((rule, i) => (
                <TableRow key={rule.id}>
                  <TableCell>
                    <StatusBadge tone={modes[rule.mode].tone}>{modes[rule.mode].badge}</StatusBadge>
                  </TableCell>
                  <TableCell className="whitespace-normal">
                    <p className="font-medium">{rule.name}</p>
                    <p className="text-sm text-fg-tertiary">{summary(rule)}</p>
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <IconButton label={`Move ${rule.name} up`} size="icon-sm" disabled={i === 0 || order.isPending} onClick={() => move(items, i, -1)}>
                        <ArrowUp />
                      </IconButton>
                      <IconButton label={`Move ${rule.name} down`} size="icon-sm" disabled={i === items.length - 1 || order.isPending} onClick={() => move(items, i, 1)}>
                        <ArrowDown />
                      </IconButton>
                      <IconButton label={`Edit ${rule.name}`} size="icon-sm" onClick={() => open(rule)}>
                        <Pencil />
                      </IconButton>
                      <IconButton label={`Delete ${rule.name}`} size="icon-sm" onClick={() => setDeleting(rule)}>
                        <Trash2 />
                      </IconButton>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
        wide
        title={editing === "new" ? "New mail flow rule" : "Edit mail flow rule"}
        description="Text matching ignores letter case. Every change is recorded in the audit log with the old and new rule."
        submitLabel="Save rule"
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => {
          const refused = lockedFor && !v.reject ? flowTargets.filter((name) => (name === "approvers" ? v.approvers.some((a) => a.address.toLowerCase() === lockedFor) : v[name].length > 0)) : []
          for (const name of refused) form.setError(name, { message: `Remove ${name === "approvers" ? "your own address" : "these addresses"}, or ask an owner or administrator of this organization to grant you the role first.` })
          if (!refused.length) save.mutate(v)
        })}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="flow-name" label="Name" error={errors.name?.message}>
            <Input id="flow-name" autoComplete="off" aria-invalid={!!errors.name} {...form.register("name")} />
          </Field>
          <Controller
            control={form.control}
            name="mode"
            render={({ field }) => (
              <Field id="flow-mode" label="Mode" hint="Test mode records matches below without changing any message.">
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger id="flow-mode" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(Object.keys(modes) as (keyof typeof modes)[]).map((m) => (
                      <SelectItem key={m} value={m}>
                        {modes[m].label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            )}
          />
        </div>
        <Conditions control={form.control} register={form.register} errors={errors.conditions} name="conditions" legend="Apply when all of these match" empty="No conditions: the rule applies to every message." />
        <Conditions control={form.control} register={form.register} errors={errors.exceptions} name="exceptions" legend="Except when any of these match" empty="No exceptions." />

        <fieldset className="flex flex-col gap-4 border-t border-border pt-4">
          <legend className="sr-only">Actions</legend>
          <SwitchField control={form.control} name="reject" id="flow-reject" label="Reject the message" hint="The sender gets your message in the bounce. A rule that rejects has no other actions." />
          {reject ? (
            <Field id="flow-reject-message" label="Rejection message" hint="Plain ASCII, one line." error={errors.rejectMessage?.message}>
              <Input id="flow-reject-message" maxLength={200} autoComplete="off" aria-invalid={!!errors.rejectMessage} {...form.register("rejectMessage")} />
            </Field>
          ) : (
            <>
              <SwitchField control={form.control} name="disclaimer" id="flow-disclaimer" label="Append a disclaimer" hint="Added below the text of the message, in both its plain text and HTML versions." />
              {disclaimer && (
                <div className="flex flex-col gap-4 rounded-lg border border-border p-4">
                  <Field id="flow-disclaimer-text" label="Text" error={errors.disclaimerText?.message}>
                    <Textarea id="flow-disclaimer-text" rows={3} aria-invalid={!!errors.disclaimerText} {...form.register("disclaimerText")} />
                  </Field>
                  <Field id="flow-disclaimer-html" label="HTML" hint="Optional. Used in HTML messages instead of the text.">
                    <Textarea id="flow-disclaimer-html" rows={3} spellCheck={false} className="font-mono text-sm" {...form.register("disclaimerHtml")} />
                  </Field>
                  <Controller
                    control={form.control}
                    name="fallback"
                    render={({ field }) => (
                      <Field id="flow-fallback" label="Signed or encrypted messages" hint="Changing their text would break the signature or cannot be done.">
                        <Select value={field.value} onValueChange={field.onChange}>
                          <SelectTrigger id="flow-fallback" className="w-full">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="wrap">Attach the disclaimer as a separate part</SelectItem>
                            <SelectItem value="skip">Send without the disclaimer</SelectItem>
                            <SelectItem value="reject">Reject the message</SelectItem>
                          </SelectContent>
                        </Select>
                      </Field>
                    )}
                  />
                </div>
              )}
              {lockedFor && <LockNote>Sending copies, redirecting mail and approving held messages yourself need the owner or administrator role in this organization. Ask one of its owners or administrators to grant you the role.</LockNote>}
              {flowTargets.map((name) => (
                <Controller
                  key={name}
                  control={form.control}
                  name={name}
                  render={({ field, fieldState }) => (
                    <Field
                      id={`flow-${name}`}
                      label={{ bcc: "Send a copy to", redirectTo: "Redirect to", approvers: "Hold for approval by" }[name]}
                      hint={
                        {
                          bcc: "Recipients don’t see the copy.",
                          redirectTo: "Replaces the recipients. For incoming mail, only this organization’s recipients are replaced.",
                          approvers: "Members of this organization. Any one of them can approve the message under Settings.",
                        }[name]
                      }
                      error={fieldState.error?.message ?? fieldState.error?.root?.message}
                    >
                      <RecipientInput id={`flow-${name}`} value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
                    </Field>
                  )}
                />
              ))}
              <fieldset className="flex flex-col gap-2">
                <legend className="mb-2 text-label text-fg-secondary">Add headers</legend>
                {headers.fields.map((h, i) => (
                  <div key={h.id} className="flex flex-wrap items-start gap-2">
                    <div className="flex w-48 flex-col gap-1">
                      <Input aria-label={`Header ${i + 1} name`} placeholder="X-Department" autoComplete="off" spellCheck={false} aria-invalid={!!errors.addHeaders?.[i]?.name} {...form.register(`addHeaders.${i}.name`)} />
                      {errors.addHeaders?.[i]?.name && <p role="alert" className="text-sm text-danger">{errors.addHeaders[i].name.message}</p>}
                    </div>
                    <div className="flex min-w-36 flex-1 flex-col gap-1">
                      <Input aria-label={`Header ${i + 1} value`} autoComplete="off" aria-invalid={!!errors.addHeaders?.[i]?.value} {...form.register(`addHeaders.${i}.value`)} />
                      {errors.addHeaders?.[i]?.value && <p role="alert" className="text-sm text-danger">{errors.addHeaders[i].value.message}</p>}
                    </div>
                    <IconButton type="button" label={`Remove header ${i + 1}`} onClick={() => headers.remove(i)}>
                      <X />
                    </IconButton>
                  </div>
                ))}
                <Button type="button" variant="ghost" className="self-start" disabled={headers.fields.length >= 10} onClick={() => headers.append({ name: "", value: "" })}>
                  <Plus aria-hidden />
                  Add header
                </Button>
              </fieldset>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field id="flow-remove-headers" label="Remove headers" hint="Separate names with commas.">
                  <Input id="flow-remove-headers" autoComplete="off" spellCheck={false} {...form.register("removeHeaders")} />
                </Field>
                <Field id="flow-spam" label="Change the spam score by" hint="Optional. Positive numbers make spam filing more likely." error={errors.spamScore?.message}>
                  <Input id="flow-spam" inputMode="decimal" autoComplete="off" aria-invalid={!!errors.spamScore} {...form.register("spamScore")} />
                </Field>
              </div>
              <SwitchField control={form.control} name="requireTls" id="flow-tls" label="Require TLS for delivery" hint="Delivery waits until the receiving server offers TLS. Messages that also go to people on this server are rejected." />
            </>
          )}
          <SwitchField control={form.control} name="stop" id="flow-stop" label="Stop processing more rules" hint="Later rules of this organization are skipped for messages this rule matches." />
        </fieldset>
      </FormDialog>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete “${deleting?.name ?? ""}”?`}
        description="Messages from now on are no longer handled by this rule. Messages it holds for approval keep waiting."
        confirmLabel="Delete rule"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </section>
  )
}

function Conditions({
  control,
  register,
  errors,
  name,
  legend,
  empty,
}: {
  control: Control<Values>
  register: UseFormRegister<Values>
  errors: FieldErrors<Values>["conditions"]
  name: "conditions" | "exceptions"
  legend: string
  empty: string
}) {
  const list = useFieldArray({ control, name })
  const rows = useWatch({ control, name })

  return (
    <fieldset className="flex flex-col gap-3">
      <legend className="mb-2 text-label text-fg-secondary">{legend}</legend>
      {list.fields.length === 0 && <p className="text-sm text-fg-tertiary">{empty}</p>}
      {list.fields.map((c, i) => {
        const kind = rows?.[i]?.field ?? c.field
        const scope = rows?.[i]?.scope ?? c.scope
        const rowErrors = errors?.[i]
        const label = `${name === "conditions" ? "Condition" : "Exception"} ${i + 1}`
        return (
          <div key={c.id} className="flex flex-wrap items-start gap-2">
            <Controller
              control={control}
              name={`${name}.${i}.field`}
              render={({ field }) => (
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger aria-label={`${label} field`} className="w-56 shrink-0">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(Object.keys(fields) as FlowField[]).map((f) => (
                      <SelectItem key={f} value={f}>
                        {fields[f]}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
            {addressed(kind) && (
              <Controller
                control={control}
                name={`${name}.${i}.scope`}
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger aria-label={`${label} match`} className="w-56 shrink-0">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(Object.keys(scopes) as FlowScope[]).map((s) => (
                        <SelectItem key={s} value={s}>
                          {scopes[s]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            )}
            {kind === "header" && (
              <div className="flex w-36 flex-col gap-1">
                <Input aria-label={`${label} header name`} placeholder="List-Id" autoComplete="off" spellCheck={false} aria-invalid={!!rowErrors?.header} {...register(`${name}.${i}.header`)} />
                {rowErrors?.header && <p role="alert" className="text-sm text-danger">{rowErrors.header.message}</p>}
              </div>
            )}
            {numeric(kind) ? (
              <div className="flex flex-1 flex-col gap-1">
                <div className="flex items-center gap-2">
                  <Input aria-label={`${label} ${kind === "sizeOver" ? "size in KB" : "score"}`} inputMode="decimal" autoComplete="off" className="w-32" aria-invalid={!!rowErrors?.number} {...register(`${name}.${i}.number`)} />
                  {kind === "sizeOver" && <span className="text-fg-secondary">KB</span>}
                </div>
                {rowErrors?.number && <p role="alert" className="text-sm text-danger">{rowErrors.number.message}</p>}
              </div>
            ) : kind === "direction" ? (
              <Controller
                control={control}
                name={`${name}.${i}.values`}
                render={({ field }) => (
                  <div className="flex min-w-36 flex-1 flex-col gap-1">
                    <Select value={field.value || undefined} onValueChange={field.onChange}>
                      <SelectTrigger aria-label={`${label} direction`} className="w-full" aria-invalid={!!rowErrors?.values}>
                        <SelectValue placeholder="Choose a direction" />
                      </SelectTrigger>
                      <SelectContent>
                        {Object.entries(directions).map(([value, text]) => (
                          <SelectItem key={value} value={value}>
                            {text}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    {rowErrors?.values && <p role="alert" className="text-sm text-danger">{rowErrors.values.message}</p>}
                  </div>
                )}
              />
            ) : addressed(kind) && (scope === "inside" || scope === "outside") ? (
              <span className="flex-1" />
            ) : (
              <div className="flex min-w-36 flex-1 flex-col gap-1">
                <Input
                  aria-label={`${label} values`}
                  placeholder={addressed(kind) ? (scope === "domain" ? "example.com" : "name@example.com") : "Separate values with commas"}
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={!!rowErrors?.values}
                  {...register(`${name}.${i}.values`)}
                />
                {rowErrors?.values && <p role="alert" className="text-sm text-danger">{rowErrors.values.message}</p>}
              </div>
            )}
            <IconButton type="button" label={`Remove ${label.toLowerCase()}`} onClick={() => list.remove(i)}>
              <X />
            </IconButton>
          </div>
        )
      })}
      <Button type="button" variant="ghost" className="self-start" disabled={list.fields.length >= 20} onClick={() => list.append(toCondition({ field: name === "conditions" ? "direction" : "sender", values: name === "conditions" ? ["outgoing"] : [] }))}>
        <Plus aria-hidden />
        {name === "conditions" ? "Add condition" : "Add exception"}
      </Button>
    </fieldset>
  )
}

function MatchLog({ orgId }: { orgId: string }) {
  const matches = useQuery({ queryKey: ["mail-flow-matches", orgId], queryFn: () => api<FlowMatch[]>(`/organizations/${orgId}/mail-flow/matches`) })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Recent matches"
        description="The last 200 messages a rule matched in the past 30 days, including rules in test mode."
        actions={
          <Button variant="secondary" loading={matches.isFetching && !matches.isPending} onClick={() => matches.refetch()}>
            Refresh
          </Button>
        }
      />
      <QueryView query={matches} empty={<EmptyState icon={ScrollText} title="No matches yet" description="When a rule matches a message, it is listed here." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-44">Time</TableHead>
                <TableHead>Rule</TableHead>
                <TableHead>Message</TableHead>
                <TableHead>Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((m) => (
                <TableRow key={m.id}>
                  <TableCell>{formatDateTime(m.createdAt)}</TableCell>
                  <TableCell className="whitespace-normal">
                    <div className="flex flex-col gap-1">
                      <span className="font-medium">{m.ruleName}</span>
                      {m.test && <StatusBadge tone="info">Test only</StatusBadge>}
                    </div>
                  </TableCell>
                  <TableCell className="whitespace-normal">
                    <p>{m.subject || "(no subject)"}</p>
                    <p className="text-sm text-fg-tertiary">
                      {directions[m.direction]} · {m.sender || "empty sender"} to {m.recipients.join(", ")}
                    </p>
                  </TableCell>
                  <TableCell className="whitespace-normal text-sm">{m.actions.join(", ")}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>
    </section>
  )
}

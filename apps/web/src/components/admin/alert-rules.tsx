"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil, Send } from "lucide-react"
import { Fragment, useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { SegmentedControl } from "@/components/segmented-control"
import { QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { DialogClose } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { alertCategories, alertDocsUrl, alertSeverities } from "@/lib/alerts"
import { api, ApiError, errorMessage } from "@/lib/api"
import { formatDuration, formatListDate, formatMeasure, plural } from "@/lib/format"
import { alertRulesQuery } from "@/lib/queries"
import type { Address, AlertCategory, AlertRule, AlertSeverity, AlertTestResult } from "@/lib/types"

const number = /^\d+(\.\d+)?$/

const ruleSchema = z.object({
  severity: z.enum(["critical", "warning", "info"]),
  threshold: z.string().trim().regex(number, "Enter a number, 0 or greater."),
  cooldown: z
    .string()
    .trim()
    .regex(number, "Enter a number of minutes.")
    .refine((v) => Number(v) >= 1 && Number(v) <= 10_080, "Enter between 1 and 10080 minutes."),
  notifyEmail: z.boolean(),
  notifyWebhook: z.boolean(),
})

type RuleValues = z.infer<typeof ruleSchema>

const serverFields: Record<string, keyof RuleValues> = { invalid_threshold: "threshold", invalid_cooldown: "cooldown" }

const severityOptions = Object.entries(alertSeverities).map(([value, { label }]) => ({ value: value as AlertSeverity, label }))

const neverChecked = (rule: AlertRule) => rule.enabled && rule.lastEvaluatedAt === null

const threshold = (rule: AlertRule) => `${rule.comparator === "gte" ? "≥" : "≤"} ${formatMeasure(rule.threshold, rule.unit)}`

function channels(rule: AlertRule) {
  const email = rule.emailRecipients.length ? `email to ${plural(rule.emailRecipients.length, "recipient")}` : "email"
  const list = [rule.notifyEmail && email, rule.notifyWebhook && "webhooks"].filter(Boolean).join(", ")
  return list ? list.charAt(0).toUpperCase() + list.slice(1) : "Nobody"
}

export function AlertRules() {
  const qc = useQueryClient()
  const rules = useQuery(alertRulesQuery)
  const [editing, setEditing] = useState<AlertRule | null>(null)
  const [testing, setTesting] = useState(false)

  const toggle = useMutation({
    mutationFn: (rule: AlertRule) => api<AlertRule>(`/admin/alerts/rules/${rule.key}`, { method: "PATCH", body: { enabled: !rule.enabled } }),
    onSuccess: (saved) => {
      toast.success(`${saved.name} turned ${saved.enabled ? "on" : "off"}`)
      qc.invalidateQueries({ queryKey: ["alert-rules"] })
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Alert rules"
        description="What Aether checks, when a rule fires and who hears about it."
        actions={
          <Button variant="secondary" onClick={() => setTesting(true)} disabled={!rules.data}>
            <Send aria-hidden />
            Send test alert
          </Button>
        }
      />
      <QueryView query={rules}>
        {(items) => (
          <div className="flex flex-col gap-6">
            <NeverChecked rules={items.filter(neverChecked)} />
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Rule</TableHead>
                  <TableHead className="max-md:hidden">Fires at</TableHead>
                  <TableHead className="max-md:hidden">Severity</TableHead>
                  <TableHead className="max-lg:hidden">Notifies</TableHead>
                  <TableHead className="max-lg:hidden">Last checked</TableHead>
                  <TableHead className="w-28">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(Object.keys(alertCategories) as AlertCategory[]).map((category) => (
                  <Fragment key={category}>
                    <TableRow className="hover:bg-transparent">
                      <TableHead colSpan={6} className="h-auto bg-muted/50 py-2 text-fg-secondary">
                        {alertCategories[category]}
                      </TableHead>
                    </TableRow>
                    {items
                      .filter((r) => r.category === category)
                      .map((rule) => (
                        <TableRow key={rule.key}>
                          <TableCell className="whitespace-normal">
                            <div className="flex flex-col gap-1">
                              <div className="flex flex-wrap items-center gap-2">
                                <span className="font-medium">{rule.name}</span>
                                {neverChecked(rule) && (
                                  <StatusBadge tone={rule.scope === "domain" ? "neutral" : "warning"}>{rule.scope === "domain" ? "Not checked yet" : "Not configured"}</StatusBadge>
                                )}
                              </div>
                              <p className="max-w-[64ch] text-sm text-fg-tertiary">{rule.description}</p>
                              <p className="text-sm text-fg-secondary md:hidden">
                                {threshold(rule)} · {alertSeverities[rule.severity].label}
                              </p>
                            </div>
                          </TableCell>
                          <TableCell className="tabular-nums max-md:hidden">{threshold(rule)}</TableCell>
                          <TableCell className="max-md:hidden">{alertSeverities[rule.severity].label}</TableCell>
                          <TableCell className="max-lg:hidden">
                            <div className="flex flex-col">
                              <span>{channels(rule)}</span>
                              <span className="text-sm text-fg-tertiary">Again after {formatDuration(rule.cooldownSeconds)}</span>
                            </div>
                          </TableCell>
                          <TableCell className="text-fg-secondary max-lg:hidden">{rule.lastEvaluatedAt ? formatListDate(rule.lastEvaluatedAt) : "Never"}</TableCell>
                          <TableCell>
                            <div className="flex items-center justify-end gap-3">
                              <Switch
                                aria-label={`Enable ${rule.name}`}
                                checked={rule.enabled}
                                disabled={toggle.isPending}
                                onCheckedChange={() => toggle.mutate(rule)}
                              />
                              <IconButton label={`Edit ${rule.name}`} size="icon-sm" onClick={() => setEditing(rule)}>
                                <Pencil />
                              </IconButton>
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                  </Fragment>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </QueryView>

      {editing && <RuleDialog key={editing.key} rule={editing} onClose={() => setEditing(null)} />}
      {testing && rules.data && <TestDialog rules={rules.data} onClose={() => setTesting(false)} />}
    </section>
  )
}

function NeverChecked({ rules }: { rules: AlertRule[] }) {
  if (rules.length === 0) return null
  return (
    <div className="flex flex-col gap-1 rounded-xl border border-border bg-sunken p-4 text-sm text-fg-secondary">
      <p className="font-semibold text-foreground">{rules.length === 1 ? "1 rule is on but has" : `${rules.length} rules are on but have`} never been checked</p>
      <p className="max-w-[88ch]">
        A rule cannot fire until Aether can read its data source, which is set in the API&apos;s environment.
        {rules.some((r) => r.scope === "domain") && " Rules for domains start once a domain is verified."}{" "}
        <a href={alertDocsUrl} target="_blank" rel="noreferrer" className="font-medium text-link underline underline-offset-3 hover:text-ember">
          Set up the data sources
        </a>
      </p>
    </div>
  )
}

function RuleDialog({ rule, onClose }: { rule: AlertRule; onClose: () => void }) {
  const qc = useQueryClient()
  const minutes = rule.unit === "seconds"
  const form = useForm<RuleValues>({
    resolver: zodResolver(ruleSchema),
    defaultValues: {
      severity: rule.severity,
      threshold: String(minutes ? rule.threshold / 60 : rule.threshold),
      cooldown: String(rule.cooldownSeconds / 60),
      notifyEmail: rule.notifyEmail,
      notifyWebhook: rule.notifyWebhook,
    },
  })
  const [recipients, setRecipients] = useState<Address[]>(rule.emailRecipients.map((address) => ({ address })))
  const notifyEmail = useWatch({ control: form.control, name: "notifyEmail" })
  const errors = form.formState.errors

  const save = useMutation({
    mutationFn: (v: RuleValues) =>
      api<AlertRule>(`/admin/alerts/rules/${rule.key}`, {
        method: "PATCH",
        body: {
          severity: v.severity,
          threshold: Number(v.threshold) * (minutes ? 60 : 1),
          cooldownSeconds: Math.round(Number(v.cooldown) * 60),
          notifyEmail: v.notifyEmail,
          notifyWebhook: v.notifyWebhook,
          emailRecipients: recipients.map((r) => r.address),
        },
      }),
    meta: { silent: true },
    onSuccess: (saved) => {
      toast.success(`Saved ${saved.name}`)
      qc.invalidateQueries({ queryKey: ["alert-rules"] })
      onClose()
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 422) form.setError(serverFields[e.code] ?? "root", { message: e.message })
      else toast.error(errorMessage(e))
    },
  })

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={`Edit ${rule.name}`}
      description={rule.description}
      submitLabel="Save changes"
      pending={save.isPending}
      onSubmit={form.handleSubmit((v) => save.mutate(v))}
    >
      <Controller
        control={form.control}
        name="severity"
        render={({ field }) => (
          <fieldset className="flex flex-col items-start gap-2">
            <legend className="mb-2 text-label text-fg-secondary">Severity</legend>
            <SegmentedControl label="Severity" options={severityOptions} value={field.value} onChange={field.onChange} />
          </fieldset>
        )}
      />
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          id="rule-threshold"
          label={`Threshold in ${minutes ? "minutes" : rule.unit}`}
          hint={rule.comparator === "gte" ? "Fires at this value or above." : "Fires at this value or below."}
          error={errors.threshold?.message}
        >
          <Input id="rule-threshold" inputMode="decimal" autoComplete="off" aria-invalid={!!errors.threshold} {...form.register("threshold")} />
        </Field>
        <Field id="rule-cooldown" label="Notify again after, in minutes" hint="While the alert keeps firing unacknowledged." error={errors.cooldown?.message}>
          <Input id="rule-cooldown" inputMode="decimal" autoComplete="off" aria-invalid={!!errors.cooldown} {...form.register("cooldown")} />
        </Field>
      </div>
      <SwitchField control={form.control} name="notifyEmail" id="rule-email" label="Notify by email" hint="Sent from the mailbox set as AETHER_ALERT_FROM." />
      {notifyEmail && (
        <Field
          id="rule-recipients"
          label="Email recipients"
          hint={`Leave empty to send to every active platform administrator${rule.scope === "domain" ? " and the owners and administrators of the domain's organization" : ""}.`}
        >
          <RecipientInput id="rule-recipients" value={recipients} onChange={setRecipients} />
        </Field>
      )}
      <SwitchField
        control={form.control}
        name="notifyWebhook"
        id="rule-webhook"
        label="Notify webhooks"
        hint={
          rule.scope === "domain"
            ? "Sent to the webhooks of the domain's organization."
            : "Sent to webhooks created by a platform administrator that list the event by name, for example alert.firing."
        }
      />
      {errors.root && (
        <p role="alert" className="text-sm text-danger">
          {errors.root.message}
        </p>
      )}
    </FormDialog>
  )
}

function TestDialog({ rules, onClose }: { rules: AlertRule[]; onClose: () => void }) {
  const [ruleKey, setRuleKey] = useState("default")
  const rule = rules.find((r) => r.key === ruleKey)
  const test = useMutation({
    mutationFn: () => api<AlertTestResult>("/admin/alerts/test", { method: "POST", body: rule && { ruleKey } }),
  })
  const result = test.data

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={result ? "Test alert result" : "Send test alert"}
      description={
        result
          ? "This is what Aether did with the test alert. Nothing was recorded in the list of alerts."
          : "Sends a sample alert through the same email and webhook path as a real one, so you can check that it arrives."
      }
      submitLabel="Send test alert"
      pending={test.isPending}
      onSubmit={(e) => {
        e.preventDefault()
        test.mutate()
      }}
      footer={
        result && (
          <DialogClose asChild>
            <Button type="button">Done</Button>
          </DialogClose>
        )
      }
    >
      {result ? (
        <dl className="animate-content flex flex-col rounded-xl border border-border">
          <Outcome label="Email">
            {result.emailSent ? (
              <>
                <StatusBadge tone="success">Sent</StatusBadge>
                <span className="font-mono break-all">{result.emailRecipients.join(", ")}</span>
              </>
            ) : result.emailError ? (
              <>
                <StatusBadge tone="danger">Not sent</StatusBadge>
                <span>{result.emailError.charAt(0).toUpperCase() + result.emailError.slice(1)}</span>
              </>
            ) : (
              <>
                <StatusBadge tone="neutral">Off</StatusBadge>
                <span>Email is turned off for this rule.</span>
              </>
            )}
          </Outcome>
          <Outcome label="Webhooks">
            {result.webhooksQueued > 0 ? (
              <>
                <StatusBadge tone="success">Queued</StatusBadge>
                <span>Queued for {plural(result.webhooksQueued, "webhook")}. Aether sends it within 15 seconds.</span>
              </>
            ) : rule && !rule.notifyWebhook ? (
              <>
                <StatusBadge tone="neutral">Off</StatusBadge>
                <span>Webhooks are turned off for this rule.</span>
              </>
            ) : (
              <>
                <StatusBadge tone="warning">None queued</StatusBadge>
                <span>No webhook created by a platform administrator lists the alert.test event.</span>
              </>
            )}
          </Outcome>
        </dl>
      ) : (
        <Field id="test-rule" label="Channels and recipients" hint="Choose a rule to test its own channels and recipients.">
          <Select value={ruleKey} onValueChange={setRuleKey}>
            <SelectTrigger id="test-rule" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="default">Email and webhooks, to platform administrators</SelectItem>
              {rules.map((r) => (
                <SelectItem key={r.key} value={r.key}>
                  {r.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
    </FormDialog>
  )
}

function Outcome({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-2 border-b border-border p-3 last:border-b-0 sm:flex-row sm:gap-4">
      <dt className="w-28 shrink-0 pt-1 text-label text-fg-tertiary">{label}</dt>
      <dd className="flex min-w-0 flex-col items-start gap-2 text-sm text-fg-secondary">{children}</dd>
    </div>
  )
}

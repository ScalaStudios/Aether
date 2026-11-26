"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { z } from "zod"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { Notice } from "@/components/notice"
import { SectionHeader } from "@/components/page-header"
import { QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { formatBytes, MB, megabytes } from "@/lib/format"
import { limitsQuery, organizationDefaultsQuery, organizationsQuery } from "@/lib/queries"
import type { OrganizationLimits, QuotaDefault } from "@/lib/types"

const GB = 1024 * MB

const card = "flex flex-col gap-4 rounded-xl border border-border bg-card p-6"

const counts = [
  { used: "mailboxes", max: "maxMailboxes", label: "Mailboxes" },
  { used: "aliases", max: "maxAliases", label: "Aliases" },
  { used: "lists", max: "maxLists", label: "Distribution lists" },
  { used: "domains", max: "maxDomains", label: "Domains" },
] as const

const count = z.string().regex(/^\d*$/, "Enter a whole number, or leave it empty.")

const schema = z.object({
  maxMailboxes: count,
  maxAliases: count,
  maxLists: count,
  maxDomains: count,
  storageGb: z.string().regex(/^\d*(\.\d+)?$/, "Enter a number of gigabytes, such as 50 or 0.5, or leave it empty."),
})

type Values = z.infer<typeof schema>

const text = (n: number) => (n ? String(n) : "")

export function OrganizationLimits({ orgId }: { orgId: string }) {
  return (
    <div className="flex flex-col gap-6">
      <Limits orgId={orgId} />
      <DefaultQuota orgId={orgId} />
    </div>
  )
}

function Limits({ orgId }: { orgId: string }) {
  const platformAdmin = useSession().data?.user.isPlatformAdmin
  const limits = useQuery(limitsQuery(orgId))
  const [editing, setEditing] = useState(false)

  return (
    <section className={card}>
      <SectionHeader
        title="Limits"
        description={`How much of its allowance this organization is using. Storage counts the quotas given to mailboxes, not the disk space they fill. ${platformAdmin ? "Only platform administrators can change the limits." : "A platform administrator sets the limits."}`}
        actions={
          platformAdmin && (
            <Button variant="secondary" disabled={!limits.data} onClick={() => setEditing(true)}>
              <Pencil aria-hidden />
              Edit limits
            </Button>
          )
        }
      />
      <QueryView query={limits} skeleton={<RowsSkeleton rows={2} />}>
        {(l) => (
          <div className="flex flex-col gap-4">
            <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5">
              {counts.map(({ used, max, label }) => (
                <Usage key={max} label={label} used={l.usage[used]} max={l[max]} />
              ))}
              <Usage label="Storage" used={l.usage.storageBytes} max={l.maxStorageBytes} format={formatBytes} />
            </dl>
            {l.maxStorageBytes > 0 && l.usage.mailboxesWithoutQuota > 0 && (
              <Notice tone="warning">
                {l.usage.mailboxesWithoutQuota === 1 ? "1 mailbox has" : `${l.usage.mailboxesWithoutQuota} mailboxes have`} no quota. A mailbox without a quota is not counted against the{" "}
                {formatBytes(l.maxStorageBytes)} storage limit and can grow past it. Give every mailbox a quota under Mailboxes.
              </Notice>
            )}
          </div>
        )}
      </QueryView>
      {editing && limits.data && <LimitsDialog orgId={orgId} limits={limits.data} onClose={() => setEditing(false)} />}
    </section>
  )
}

function Usage({ label, used, max, format = String }: { label: string; used: number; max: number; format?: (n: number) => string }) {
  const ratio = max ? used / max : 0

  return (
    <div className="flex flex-col gap-2 rounded-xl border border-border p-4">
      <dt className="text-label text-fg-tertiary">{label}</dt>
      <dd className="flex flex-col gap-3">
        <span className="flex flex-wrap items-baseline gap-x-2">
          <span className="text-h3 tabular-nums">{format(used)}</span>
          <span className="text-sm text-fg-tertiary">{max ? `of ${format(max)}` : "No limit"}</span>
        </span>
        {max > 0 && (
          <>
            <span aria-hidden className="h-2 overflow-hidden rounded-full bg-border">
              <span
                className={cn("block h-full rounded-full", ratio >= 1 ? "bg-danger" : ratio >= 0.9 ? "bg-warning" : "bg-fg-secondary")}
                style={{ width: `${Math.min(ratio, 1) * 100}%` }}
              />
            </span>
            <span className="flex min-h-7 items-center text-sm text-fg-tertiary">
              {ratio >= 1 ? <StatusBadge tone="danger">{ratio > 1 ? "Over the limit" : "Limit reached"}</StatusBadge> : `${format(max - used)} left`}
            </span>
          </>
        )}
      </dd>
    </div>
  )
}

function LimitsDialog({ orgId, limits, onClose }: { orgId: string; limits: OrganizationLimits; onClose: () => void }) {
  const qc = useQueryClient()
  const orgName = useQuery({ ...organizationsQuery, select: (orgs) => orgs.find((o) => o.id === orgId)?.name }).data
  const storageGb = text(+(limits.maxStorageBytes / GB).toFixed(3))
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      maxMailboxes: text(limits.maxMailboxes),
      maxAliases: text(limits.maxAliases),
      maxLists: text(limits.maxLists),
      maxDomains: text(limits.maxDomains),
      storageGb,
    },
  })
  const errors = form.formState.errors

  const save = useMutation({
    mutationFn: (v: Values) =>
      api<OrganizationLimits>(`/organizations/${orgId}/limits`, {
        method: "PUT",
        body: {
          maxMailboxes: Number(v.maxMailboxes),
          maxAliases: Number(v.maxAliases),
          maxLists: Number(v.maxLists),
          maxDomains: Number(v.maxDomains),
          maxStorageBytes: v.storageGb === storageGb ? limits.maxStorageBytes : Math.round(Number(v.storageGb) * GB),
        },
      }),
    onSuccess: (saved) => {
      qc.setQueryData(limitsQuery(orgId).queryKey, saved)
      toast.success("Limits saved")
      onClose()
    },
  })

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={`Limits for ${orgName ?? "this organization"}`}
      description="Leave a field empty for no limit. A limit below current use deletes nothing. It stops anything new from being added until use is back under the limit."
      submitLabel="Save limits"
      pending={save.isPending}
      onSubmit={form.handleSubmit((v) => save.mutate(v))}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        {counts.map(({ used, max, label }) => (
          <Field key={max} id={`limit-${used}`} label={label} hint={`${limits.usage[used]} in use`} error={errors[max]?.message}>
            <Input id={`limit-${used}`} inputMode="numeric" autoComplete="off" maxLength={9} placeholder="No limit" aria-invalid={!!errors[max]} {...form.register(max)} />
          </Field>
        ))}
      </div>
      <Field
        id="limit-storage"
        label="Storage in GB"
        hint={`${formatBytes(limits.usage.storageBytes)} is given to mailboxes now. While a storage limit is set, every new mailbox needs a quota.`}
        error={errors.storageGb?.message}
      >
        <Input id="limit-storage" inputMode="decimal" autoComplete="off" maxLength={9} placeholder="No limit" aria-invalid={!!errors.storageGb} {...form.register("storageGb")} />
      </Field>
    </FormDialog>
  )
}

function DefaultQuota({ orgId }: { orgId: string }) {
  const defaults = useQuery(organizationDefaultsQuery(orgId))

  return (
    <section className={card}>
      <SectionHeader
        title="Default quota"
        description="The quota a new mailbox gets when it is created without one, including the mailboxes that SCIM and single sign-on create. A domain can have its own default: open the domain under Domains. Existing mailboxes keep the quota they have."
      />
      <QueryView query={defaults} skeleton={<RowsSkeleton rows={1} />}>
        {(d) => <DefaultQuotaForm orgId={orgId} defaults={d} />}
      </QueryView>
    </section>
  )
}

function DefaultQuotaForm({ orgId, defaults }: { orgId: string; defaults: QuotaDefault }) {
  const qc = useQueryClient()
  const saved = megabytes(defaults.quotaBytes)
  const [value, setValue] = useState(saved)
  const invalid = !/^\d*$/.test(value)

  const save = useMutation({
    mutationFn: () => api<QuotaDefault>(`/organizations/${orgId}/defaults`, { method: "PUT", body: { quotaBytes: value ? Number(value) * MB : null } }),
    onSuccess: (d) => {
      qc.setQueryData(organizationDefaultsQuery(orgId).queryKey, d)
      qc.invalidateQueries({ queryKey: ["domain-defaults"] })
      toast.success(d.quotaBytes === null ? "Default quota removed" : "Default quota saved")
    },
  })

  return (
    <form
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        if (!invalid) save.mutate()
      }}
    >
      <Field
        id="default-quota"
        label="Quota in MB"
        hint={
          Number(value) > 0
            ? `New mailboxes get ${formatBytes(Number(value) * MB)}, unless their domain has its own default.`
            : "Leave empty for no default. New mailboxes then get no quota, unless their domain has its own default."
        }
        error={invalid ? "Enter a whole number of megabytes." : undefined}
      >
        <div className="flex flex-col gap-3 sm:flex-row">
          <Input
            id="default-quota"
            inputMode="numeric"
            autoComplete="off"
            maxLength={9}
            placeholder="No default"
            className="sm:max-w-sm"
            value={value}
            aria-invalid={invalid}
            onChange={(e) => setValue(e.target.value.trim())}
          />
          <Button type="submit" variant="secondary" loading={save.isPending} disabled={invalid || value === saved}>
            Save default
          </Button>
        </div>
      </Field>
    </form>
  )
}

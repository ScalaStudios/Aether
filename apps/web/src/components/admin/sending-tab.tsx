"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ShieldCheck } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import { domainsQuery } from "@/lib/queries"
import type { Mailbox, RestrictedSender, SendingLimitKey, SendingLimits, SendingLimitSettings } from "@/lib/types"

const limits: { key: SendingLimitKey; label: string; reason: string; max: number }[] = [
  { key: "messagesPerHour", label: "Messages an hour", reason: "Too many messages in an hour", max: 100000 },
  { key: "messagesPerDay", label: "Messages a day", reason: "Too many messages in a day", max: 1000000 },
  { key: "recipientsPerMessage", label: "Recipients per message", reason: "Too many recipients in one message", max: 1000 },
  { key: "recipientsPerDay", label: "Recipients a day", reason: "Too many recipients in a day", max: 1000000 },
  { key: "newRecipientsPerDay", label: "First-time recipients a day", reason: "Too many first-time recipients in a day", max: 1000000 },
  { key: "spamPercent", label: "Spam share (%)", reason: "Too much mail scored as spam", max: 100 },
]

export function SendingTab({ orgId }: { orgId: string }) {
  return (
    <div className="flex flex-col gap-12">
      <RestrictedSenders orgId={orgId} />
      <LimitSettings orgId={orgId} />
    </div>
  )
}

function RestrictedSenders({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const senders = useQuery({ queryKey: ["restricted-senders", orgId], queryFn: () => api<RestrictedSender[]>(`/organizations/${orgId}/restricted-senders`) })
  const [releasing, setReleasing] = useState<RestrictedSender | null>(null)
  const release = useMutation({
    mutationFn: (s: RestrictedSender) => api(`/mailboxes/${s.mailboxId}/sending-restriction`, { method: "DELETE" }),
    onSuccess: (_d, s) => {
      toast.success(`${s.address} can send again`)
      setReleasing(null)
      qc.invalidateQueries({ queryKey: ["restricted-senders", orgId] })
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Restricted senders"
        description="Aether stops outgoing mail from an account that goes over a sending limit, and tells the organization's administrators by email and webhook. The account can still receive and read mail."
      />
      <QueryView query={senders} empty={<EmptyState icon={ShieldCheck} title="No restricted senders" description="Every account in this organization can send." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Reason</TableHead>
                <TableHead>Restricted</TableHead>
                <TableHead className="w-28">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((s) => (
                <TableRow key={s.mailboxId}>
                  <TableCell className="font-mono text-sm">{s.address}</TableCell>
                  <TableCell>{limits.find((l) => l.key === s.reason)?.reason ?? "Sending limit exceeded"}</TableCell>
                  <TableCell>{formatDateTime(s.restrictedAt)}</TableCell>
                  <TableCell>
                    <Button variant="secondary" size="sm" onClick={() => setReleasing(s)}>
                      Release
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>
      <ConfirmDialog
        open={!!releasing}
        onOpenChange={(open) => !open && setReleasing(null)}
        title={`Release ${releasing?.address}?`}
        description="The account can send again straight away, and its counts for this hour and day start over. If it may be compromised, reset its password and revoke its app passwords first."
        confirmLabel="Release"
        pending={release.isPending}
        onConfirm={() => releasing && release.mutate(releasing)}
      />
    </section>
  )
}

function LimitSettings({ orgId }: { orgId: string }) {
  const settings = useQuery({ queryKey: ["sending-limits", orgId], queryFn: () => api<SendingLimitSettings>(`/organizations/${orgId}/sending-limits`) })
  const domains = useQuery(domainsQuery(orgId))
  const mailboxes = useQuery({ queryKey: ["mailboxes", orgId], queryFn: () => api<Mailbox[]>(`/organizations/${orgId}/mailboxes`) })
  const [scope, setScope] = useState("organization")
  const domain = domains.data?.find((d) => d.id === scope)
  const mailbox = mailboxes.data?.find((m) => m.id === scope)

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Sending limits"
        description="A mailbox uses its own limits first, then its domain's, then the organization's, then the defaults. Limits count mail sent from Aether Mail and from mail apps, per account that signs in to send."
        actions={
          <Select value={scope} onValueChange={setScope}>
            <SelectTrigger aria-label="Limits for" className="w-64">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="organization">Whole organization</SelectItem>
              {!!domains.data?.length && (
                <SelectGroup>
                  <SelectLabel>Domains</SelectLabel>
                  {domains.data.map((d) => (
                    <SelectItem key={d.id} value={d.id}>
                      @{d.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              )}
              {!!mailboxes.data?.length && (
                <SelectGroup>
                  <SelectLabel>Mailboxes</SelectLabel>
                  {mailboxes.data.map((m) => (
                    <SelectItem key={m.id} value={m.id}>
                      {m.address}
                    </SelectItem>
                  ))}
                </SelectGroup>
              )}
            </SelectContent>
          </Select>
        }
      />
      <QueryView query={settings} skeleton={<RowsSkeleton rows={4} />}>
        {(data) => {
          const domainId = domain?.id ?? mailbox?.domainId
          const parent = { ...data.defaults, ...data.organization, ...(mailbox && domainId ? data.domains[domainId] : {}) }
          const own = mailbox ? data.mailboxes[mailbox.id] : domain ? data.domains[domain.id] : data.organization
          const inherited = mailbox || domain ? parent : data.defaults
          const body = mailbox ? { mailboxId: mailbox.id } : domain ? { domainId: domain.id } : {}
          return <LimitEditor key={scope} orgId={orgId} own={own ?? {}} inherited={inherited} body={body} />
        }}
      </QueryView>
    </section>
  )
}

function LimitEditor({ orgId, own, inherited, body }: { orgId: string; own: SendingLimits; inherited: Record<SendingLimitKey, number>; body: object }) {
  const qc = useQueryClient()
  const [values, setValues] = useState<Record<SendingLimitKey, string>>(
    () => Object.fromEntries(limits.map((l) => [l.key, own[l.key]?.toString() ?? ""])) as Record<SendingLimitKey, string>
  )
  const save = useMutation({
    mutationFn: (next: Record<SendingLimitKey, string>) =>
      api(`/organizations/${orgId}/sending-limits`, {
        method: "PUT",
        body: { ...body, limits: Object.fromEntries(Object.entries(next).filter(([, v]) => v !== "").map(([k, v]) => [k, Number(v)])) },
      }),
    onSuccess: () => {
      toast.success("Sending limits saved")
      qc.invalidateQueries({ queryKey: ["sending-limits", orgId] })
    },
  })
  const invalid = limits.find((l) => values[l.key] !== "" && !(/^\d+$/.test(values[l.key]) && Number(values[l.key]) <= l.max))

  return (
    <form
      noValidate
      className="animate-content flex flex-col gap-6 rounded-xl border border-border bg-card p-6"
      onSubmit={(e) => {
        e.preventDefault()
        if (!invalid) save.mutate(values)
      }}
    >
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {limits.map((l) => (
          <Field
            key={l.key}
            id={`limit-${l.key}`}
            label={l.label}
            error={invalid?.key === l.key ? `Enter a whole number up to ${l.max.toLocaleString()}, or leave it empty.` : undefined}
            hint={`Empty uses ${inherited[l.key] ? inherited[l.key].toLocaleString() : "no limit"}. 0 means no limit.`}
          >
            <Input
              id={`limit-${l.key}`}
              inputMode="numeric"
              placeholder={inherited[l.key] ? inherited[l.key].toString() : "No limit"}
              value={values[l.key]}
              aria-invalid={invalid?.key === l.key}
              onChange={(e) => setValues((v) => ({ ...v, [l.key]: e.target.value.trim() }))}
            />
          </Field>
        ))}
      </div>
      <p className="text-sm text-fg-tertiary">
        Going over a limit for messages, daily recipients or first-time recipients restricts the account. Too many recipients in one message only refuses the extra recipients. The spam share counts once an account has sent 10 messages that day.
      </p>
      <div className="flex flex-wrap gap-3">
        <Button type="submit" variant="secondary" loading={save.isPending}>
          Save limits
        </Button>
        <Button
          type="button"
          variant="quiet"
          disabled={Object.keys(own).length === 0 || save.isPending}
          onClick={() => {
            const cleared = Object.fromEntries(limits.map((l) => [l.key, ""])) as Record<SendingLimitKey, string>
            setValues(cleared)
            save.mutate(cleared)
          }}
        >
          Clear these limits
        </Button>
      </div>
    </form>
  )
}

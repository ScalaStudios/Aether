"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Forward } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import type { Address, Forwarding, ForwardingMode } from "@/lib/types"

const modes: Record<ForwardingMode, { label: string; hint: string }> = {
  allowed: { label: "Allowed", hint: "Mail rules can forward to any address." },
  domains: { label: "Only to listed domains", hint: "Mail rules can forward only to the domains below. Other outside addresses are refused." },
  blocked: { label: "Blocked", hint: "Mail rules can’t forward outside the organization. The mail server refuses it as well." },
}

export function ForwardingTab({ orgId }: { orgId: string }) {
  const forwarding = useQuery({
    queryKey: ["forwarding", orgId],
    queryFn: () => api<Forwarding>(`/organizations/${orgId}/forwarding`),
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader title="Forwarding to outside addresses" description="Controls the forward action of mail rules. Forwarding to this organization’s own domains is always allowed." />
      <QueryView query={forwarding} skeleton={<RowsSkeleton rows={4} />}>
        {(data) => (
          <div className="flex flex-col gap-6">
            <PolicyForm key={`${data.mode}-${data.domains.join()}`} orgId={orgId} forwarding={data} />
            <Forwarders forwarding={data} />
          </div>
        )}
      </QueryView>
    </section>
  )
}

function PolicyForm({ orgId, forwarding }: { orgId: string; forwarding: Forwarding }) {
  const qc = useQueryClient()
  const [mode, setMode] = useState(forwarding.mode)
  const [domains, setDomains] = useState<Address[]>(forwarding.domains.map((d) => ({ address: `@${d}` })))
  const [error, setError] = useState("")
  const save = useMutation({
    mutationFn: () => api<Forwarding>(`/organizations/${orgId}/forwarding`, { method: "PUT", body: { mode, domains: domains.map((d) => d.address.replace(/^@/, "")) } }),
    onSuccess: (saved) => {
      qc.setQueryData(["forwarding", orgId], saved)
      toast.success("Forwarding policy saved")
    },
    onError: () => qc.invalidateQueries({ queryKey: ["forwarding", orgId] }),
  })
  const unchanged = mode === forwarding.mode && domains.map((d) => d.address).join() === forwarding.domains.map((d) => `@${d}`).join()

  return (
    <form
      noValidate
      className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6"
      onSubmit={(e) => {
        e.preventDefault()
        if (mode === "domains" && domains.length === 0) {
          setError("Add at least one domain that mail can be forwarded to.")
          return
        }
        setError("")
        save.mutate()
      }}
    >
      <Field id="forwarding-mode" label="Automatic forwarding" hint={modes[mode].hint}>
        <Select value={mode} onValueChange={(v) => setMode(v as ForwardingMode)}>
          <SelectTrigger id="forwarding-mode" className="w-full sm:w-80">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(Object.keys(modes) as ForwardingMode[]).map((m) => (
              <SelectItem key={m} value={m}>
                {modes[m].label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      {mode === "domains" && (
        <Field id="forwarding-domains" label="Allowed domains" hint="For example @partner.com. Subdomains need their own entry. Up to 100." error={error}>
          <RecipientInput id="forwarding-domains" value={domains} onChange={setDomains} invalid={!!error} placeholder="@partner.com" allowDomains />
        </Field>
      )}
      <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={unchanged}>
        Save policy
      </Button>
    </form>
  )
}

function Forwarders({ forwarding }: { forwarding: Forwarding }) {
  return (
    <div className="flex flex-col gap-4">
      <SectionHeader title="Mailboxes that forward outside" description="Every mail rule that forwards to an address outside this organization, and whether the policy lets it run." />
      {forwarding.forwarders.length === 0 ? (
        <EmptyState icon={Forward} title="No mail is forwarded outside" description="Mail rules that forward to outside addresses are listed here." />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Mailbox</TableHead>
              <TableHead>Rule</TableHead>
              <TableHead>Forwards to</TableHead>
              <TableHead>Status</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {forwarding.forwarders.map((f, i) => (
              <TableRow key={`${f.mailboxId}-${i}`}>
                <TableCell className="font-mono text-sm">{f.address}</TableCell>
                <TableCell>{f.rule}</TableCell>
                <TableCell className="font-mono text-sm break-all whitespace-normal">{f.to}</TableCell>
                <TableCell>
                  {!f.allowed ? (
                    <StatusBadge tone="danger">Blocked by policy</StatusBadge>
                  ) : f.enabled ? (
                    <StatusBadge tone="warning">Forwarding</StatusBadge>
                  ) : (
                    <StatusBadge tone="neutral">Rule off</StatusBadge>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

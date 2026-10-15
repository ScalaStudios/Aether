"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ChartColumn, ChevronRight, Globe, Inbox, Plus, RefreshCw, Trash2 } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyButton } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDate, formatPercent, plural } from "@/lib/format"
import { domainsQuery } from "@/lib/queries"
import type { DMARCSource, DMARCSummary, DNSRecord, DNSStatus, Domain } from "@/lib/types"

const schema = z.object({
  name: z
    .string()
    .trim()
    .toLowerCase()
    .regex(/^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/, "Enter a domain name such as example.com."),
})

type Values = z.infer<typeof schema>

const dnsStatus: Record<DNSStatus, { tone: "success" | "warning" | "danger"; label: string }> = {
  ok: { tone: "success", label: "OK" },
  missing: { tone: "warning", label: "Missing" },
  mismatch: { tone: "danger", label: "Mismatch" },
  error: { tone: "danger", label: "Lookup error" },
}

const purposes: Record<DNSRecord["purpose"], string> = {
  verification: "Ownership",
  mx: "Mail exchanger",
  spf: "SPF",
  dkim: "DKIM",
  dmarc: "DMARC",
  "mta-sts-host": "MTA-STS policy host",
  "mta-sts": "MTA-STS",
  "tls-rpt": "TLS reporting",
}

const periods = [7, 30, 90].map((days) => ({ value: days, label: `${days} days` }))

function aligned(s: DMARCSource) {
  return Math.max(s.dkimAligned, s.spfAligned)
}

function SourceStatusBadge({ source }: { source: DMARCSource }) {
  const passed = aligned(source)
  if (passed >= source.messages) return <StatusBadge tone="success">Passing</StatusBadge>
  if (passed === 0) return <StatusBadge tone="danger">Failing</StatusBadge>
  return <StatusBadge tone="warning">Partly failing</StatusBadge>
}

function DomainVerifiedBadge({ domain }: { domain: Domain }) {
  return domain.verified ? <StatusBadge tone="success">Verified</StatusBadge> : <StatusBadge tone="warning">Unverified</StatusBadge>
}

export function DomainsTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const domains = useQuery(domainsQuery(orgId))
  const [adding, setAdding] = useState(false)
  const [openId, setOpenId] = useState<string | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { name: "" } })

  const add = useMutation({
    mutationFn: (values: Values) => api<Domain>(`/organizations/${orgId}/domains`, { method: "POST", body: values }),
    onSuccess: (domain) => {
      qc.invalidateQueries({ queryKey: ["domains", orgId] })
      setAdding(false)
      form.reset()
      setOpenId(domain.id)
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Domains"
        description="Aether receives and sends mail for these domains once their DNS records are in place."
        actions={
          <Button onClick={() => setAdding(true)}>
            <Plus aria-hidden />
            Add domain
          </Button>
        }
      />
      <QueryView query={domains} empty={<EmptyState icon={Globe} title="No domains" description="Add a domain to start creating mailboxes on it." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Domain</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Added</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Details</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((d) => (
                <TableRow key={d.id} className="cursor-pointer" onClick={() => setOpenId(d.id)}>
                  <TableCell className="font-mono text-sm">{d.name}</TableCell>
                  <TableCell>
                    <DomainVerifiedBadge domain={d} />
                  </TableCell>
                  <TableCell>{formatDate(d.createdAt)}</TableCell>
                  <TableCell>
                    <Button variant="quiet" size="icon-sm" aria-label={`Open ${d.name}`} onClick={() => setOpenId(d.id)}>
                      <ChevronRight />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={adding}
        onOpenChange={(open) => {
          setAdding(open)
          if (!open) form.reset()
        }}
        title="Add domain"
        description="After adding it, publish the DNS records Aether lists for it."
        submitLabel="Add domain"
        pending={add.isPending}
        onSubmit={form.handleSubmit((v) => add.mutate(v))}
      >
        <Field id="domain-name" label="Domain name" error={form.formState.errors.name?.message}>
          <Input id="domain-name" placeholder="example.com" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
        </Field>
      </FormDialog>

      {openId && <DomainDialog key={openId} domainId={openId} orgId={orgId} onClose={() => setOpenId(null)} />}
    </section>
  )
}

function DomainDialog({ domainId, orgId, onClose }: { domainId: string; orgId: string; onClose: () => void }) {
  const qc = useQueryClient()
  const domain = useQuery({ queryKey: ["domain", domainId], queryFn: () => api<Domain>(`/domains/${domainId}`) })
  const dns = useQuery({ queryKey: ["dns", domainId], queryFn: () => api<{ records: DNSRecord[] }>(`/domains/${domainId}/dns`) })
  const [deleting, setDeleting] = useState(false)
  const [reporting, setReporting] = useState(false)

  const verify = useMutation({
    mutationFn: () => api<Domain>(`/domains/${domainId}/verify`, { method: "POST" }),
    onSuccess: (d) => {
      qc.setQueryData(["domain", domainId], d)
      qc.invalidateQueries({ queryKey: ["domains", orgId] })
      qc.invalidateQueries({ queryKey: ["dns", domainId] })
      toast.success(`${d.name} is verified`)
    },
  })
  const remove = useMutation({
    mutationFn: () => api(`/domains/${domainId}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Domain deleted")
      qc.invalidateQueries({ queryKey: ["domains", orgId] })
      qc.invalidateQueries({ queryKey: ["mailboxes", orgId] })
      qc.invalidateQueries({ queryKey: ["aliases", orgId] })
      onClose()
    },
  })

  const d = domain.data

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-[800px]">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-3">
            <span className="font-mono">{d?.name ?? "Domain"}</span>
            {d && <DomainVerifiedBadge domain={d} />}
          </DialogTitle>
          <DialogDescription>
            {d?.verified
              ? "Keep these records published so mail is delivered and passes SPF, DKIM and DMARC checks."
              : "Publish the ownership TXT record, wait for DNS to update, then select Verify. Lookups can take a few minutes to reflect changes."}
          </DialogDescription>
        </DialogHeader>
        <QueryView query={dns} isEmpty={(data) => data.records.length === 0} empty={<p className="text-fg-secondary">No DNS records are required for this domain.</p>}>
          {(data) => (
            <ul className="flex flex-col gap-3">
              {data.records.map((r) => (
                <li key={`${r.purpose}-${r.type}-${r.name}`} className="flex flex-col gap-3 rounded-xl border border-border bg-sunken p-4">
                  <div className="flex flex-wrap items-center gap-3">
                    <span className="rounded-xs bg-muted px-2 py-1 font-mono text-xs">{r.type}</span>
                    <span className="flex-1 font-semibold">{purposes[r.purpose]}</span>
                    <StatusBadge tone={dnsStatus[r.status].tone}>{dnsStatus[r.status].label}</StatusBadge>
                  </div>
                  <dl className="flex flex-col gap-2">
                    {[
                      ["Name", r.name],
                      ["Value", r.value],
                    ].map(([label, value]) => (
                      <div key={label} className="flex items-start gap-3">
                        <dt className="w-12 shrink-0 pt-2 text-label text-fg-tertiary">{label}</dt>
                        <dd className="min-w-0 flex-1 pt-1 font-mono text-sm break-all">{value}</dd>
                        <CopyButton value={value} label={`Copy ${label.toLowerCase()}`} />
                      </div>
                    ))}
                    {r.status !== "ok" && r.found.length > 0 && (
                      <div className="flex items-start gap-3">
                        <dt className="w-12 shrink-0 text-label text-fg-tertiary">Found</dt>
                        <dd className="min-w-0 flex-1 font-mono text-sm break-all text-fg-secondary">{r.found.join(" · ")}</dd>
                      </div>
                    )}
                  </dl>
                </li>
              ))}
            </ul>
          )}
        </QueryView>
        <DialogFooter className="sm:justify-between">
          <Button variant="secondary" onClick={() => setDeleting(true)} disabled={!d} className="text-danger">
            <Trash2 aria-hidden />
            Delete domain
          </Button>
          <div className="flex flex-col-reverse gap-3 sm:flex-row">
            <Button variant="secondary" onClick={() => setReporting(true)} disabled={!d}>
              <ChartColumn aria-hidden />
              DMARC reports
            </Button>
            <Button variant="secondary" onClick={() => dns.refetch()} loading={dns.isRefetching}>
              <RefreshCw aria-hidden />
              Check DNS again
            </Button>
            {d && !d.verified && (
              <Button onClick={() => verify.mutate()} loading={verify.isPending}>
                Verify
              </Button>
            )}
          </div>
        </DialogFooter>
        {d && reporting && <DmarcDialog domainId={domainId} domainName={d.name} onClose={() => setReporting(false)} />}
        {d && (
          <ConfirmDialog
            open={deleting}
            onOpenChange={setDeleting}
            title={`Delete ${d.name}?`}
            description="This deletes the domain with all of its mailboxes and aliases. Stored mail stays on disk until an operator removes it."
            confirmLabel="Delete domain"
            typedConfirmation={d.name}
            pending={remove.isPending}
            onConfirm={() => remove.mutate()}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DmarcDialog({ domainId, domainName, onClose }: { domainId: string; domainName: string; onClose: () => void }) {
  const [days, setDays] = useState(30)
  const dmarc = useQuery({
    queryKey: ["dmarc", domainId, days],
    queryFn: () => api<DMARCSummary>(`/domains/${domainId}/dmarc?days=${days}`),
    placeholderData: keepPreviousData,
  })
  const sources = dmarc.data?.sources ?? []
  const messages = sources.reduce((n, s) => n + s.messages, 0)
  const passed = sources.reduce((n, s) => n + aligned(s), 0)

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="grid-cols-1 sm:max-w-[960px]">
        <DialogHeader>
          <DialogTitle>DMARC reports</DialogTitle>
          <DialogDescription>
            Aggregate reports receivers sent about mail from <span className="font-mono">{domainName}</span>, grouped by sending IP.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-sm text-fg-secondary tabular-nums">
            {dmarc.data && sources.length > 0 && `${plural(dmarc.data.reports, "report")} · ${plural(messages, "message")} · about ${formatPercent(passed, messages)} passed DMARC`}
          </p>
          <SegmentedControl label="Period" options={periods} value={days} onChange={setDays} />
        </div>
        <QueryView
          query={dmarc}
          isEmpty={(data) => data.sources.length === 0}
          empty={
            <EmptyState
              icon={Inbox}
              title="No reports yet"
              description={`Receivers usually send them once a day after the DMARC record includes rua=mailto:dmarc-reports@${domainName}.`}
            />
          }
        >
          {(data) => (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Source IP</TableHead>
                  <TableHead className="text-right">Messages</TableHead>
                  <TableHead className="text-right">DKIM aligned</TableHead>
                  <TableHead className="text-right">SPF aligned</TableHead>
                  <TableHead className="text-right">Quarantined / rejected</TableHead>
                  <TableHead>Reporters</TableHead>
                  <TableHead>Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.sources.map((s) => (
                  <TableRow key={s.sourceIp}>
                    <TableCell className="font-mono text-sm">{s.sourceIp}</TableCell>
                    <TableCell className="text-right tabular-nums">{s.messages}</TableCell>
                    <TableCell className="text-right tabular-nums">{formatPercent(s.dkimAligned, s.messages)}</TableCell>
                    <TableCell className="text-right tabular-nums">{formatPercent(s.spfAligned, s.messages)}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {s.quarantined} / {s.rejected}
                    </TableCell>
                    <TableCell className="text-sm text-fg-secondary">{s.reporters.join(", ")}</TableCell>
                    <TableCell>
                      <SourceStatusBadge source={s} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </QueryView>
      </DialogContent>
    </Dialog>
  )
}

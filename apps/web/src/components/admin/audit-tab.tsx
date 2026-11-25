"use client"

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ScrollText } from "lucide-react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, ErrorState, QueryView, RowsSkeleton } from "@/components/states"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import type { AuditEvent, MailboxAuditSettings } from "@/lib/types"

const PAGE = 50

export function AuditTab({ orgId }: { orgId: string }) {
  const audit = useInfiniteQuery({
    queryKey: ["audit", orgId],
    queryFn: ({ pageParam }) => api<AuditEvent[]>(`/organizations/${orgId}/audit`, { query: { before: pageParam } }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => (last.length === PAGE ? last[last.length - 1]?.id : undefined),
  })
  const events = audit.data?.pages.flat() ?? []

  return (
    <div className="flex flex-col gap-10">
      <MailboxAuditSection orgId={orgId} />
      <section className="flex flex-col gap-6">
        <SectionHeader title="Audit log" description="Changes and sign-ins in this organization, newest first." />
        {audit.isPending ? (
          <RowsSkeleton rows={5} />
        ) : audit.isError ? (
          <ErrorState error={audit.error} onRetry={() => audit.refetch()} />
        ) : events.length === 0 ? (
          <EmptyState icon={ScrollText} title="No events yet" description="Actions such as creating mailboxes or verifying domains are recorded here." />
        ) : (
          <div className="animate-content flex flex-col gap-4">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Actor</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>IP address</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {events.map((e) => (
                  <TableRow key={e.id}>
                    <TableCell>{formatDateTime(e.createdAt)}</TableCell>
                    <TableCell>{e.actorEmail ?? "System"}</TableCell>
                    <TableCell className="font-mono text-sm">{e.action}</TableCell>
                    <TableCell className="font-mono text-sm">
                      {e.targetType}
                      <span className="text-fg-tertiary"> {e.targetId}</span>
                    </TableCell>
                    <TableCell className="font-mono text-sm">{e.ip || "—"}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            {audit.hasNextPage && (
              <Button variant="secondary" className="self-center" loading={audit.isFetchingNextPage} onClick={() => audit.fetchNextPage()}>
                Load older events
              </Button>
            )}
          </div>
        )}
      </section>
    </div>
  )
}

function MailboxAuditSection({ orgId }: { orgId: string }) {
  const settings = useQuery({ queryKey: ["mailbox-audit", orgId], queryFn: () => api<MailboxAuditSettings>(`/organizations/${orgId}/mailbox-audit`) })
  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Mailbox access"
        description="Record when delegates, shared mailbox members and administrators open, read, move, delete or send from a mailbox that is not theirs. Open a mailbox's access report from the Mailboxes tab."
      />
      <QueryView query={settings} skeleton={<RowsSkeleton rows={3} />}>
        {(data) => <MailboxAuditForm orgId={orgId} settings={data} />}
      </QueryView>
    </section>
  )
}

function MailboxAuditForm({ orgId, settings }: { orgId: string; settings: MailboxAuditSettings }) {
  const qc = useQueryClient()
  const form = useForm<MailboxAuditSettings>({ defaultValues: settings })
  const save = useMutation({
    mutationFn: (values: MailboxAuditSettings) => api<MailboxAuditSettings>(`/organizations/${orgId}/mailbox-audit`, { method: "PUT", body: values }),
    onSuccess: (saved) => {
      qc.setQueryData(["mailbox-audit", orgId], saved)
      form.reset(saved)
      toast.success("Mailbox access settings saved")
    },
  })
  const retention = form.formState.errors.retentionDays?.message

  return (
    <form className="flex flex-col gap-6 rounded-xl border border-border bg-card p-6" onSubmit={form.handleSubmit((v) => save.mutate(v))}>
      <SwitchField control={form.control} name="enabled" id="audit-enabled" label="Record mailbox access" hint="Covers Aether Mail and mail apps that connect over IMAP." />
      <SwitchField control={form.control} name="logReads" id="audit-reads" label="Include opened folders and read messages" hint="Mail apps that download every message record a read for each one." />
      <SwitchField control={form.control} name="logOwnerDeletes" id="audit-owner-deletes" label="Include deletions by the owner" hint="Applies to mail app sessions that start after the change." />
      <SwitchField control={form.control} name="ownersCanView" id="audit-owners" label="Let owners see who accessed their mailbox" hint="Owners find the report in Settings." />
      <Field id="audit-retention" label="Keep entries for (days)" error={retention}>
        <Input
          id="audit-retention"
          type="number"
          inputMode="numeric"
          min={1}
          max={3650}
          className="max-w-40"
          aria-invalid={!!retention}
          {...form.register("retentionDays", {
            valueAsNumber: true,
            validate: (v) => (Number.isInteger(v) && v >= 1 && v <= 3650) || "Enter a whole number of days from 1 to 3650.",
          })}
        />
      </Field>
      <Button type="submit" className="self-start" loading={save.isPending} disabled={!form.formState.isDirty}>
        Save
      </Button>
    </form>
  )
}

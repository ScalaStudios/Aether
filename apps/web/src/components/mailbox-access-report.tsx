"use client"

import { useInfiniteQuery } from "@tanstack/react-query"
import { ShieldCheck } from "lucide-react"
import { useState } from "react"

import { Field } from "@/components/field"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDateTime, plural } from "@/lib/format"
import type { AccessRole, MailboxAccessEvent, MailboxAccessReport as Report } from "@/lib/types"

const PAGE = 100

const actionLabels: Record<string, string> = {
  "folder.open": "Opened folder",
  "message.read": "Read message",
  "message.move": "Moved",
  "message.copy": "Copied",
  "message.delete": "Deleted",
  "message.send_as": "Sent as",
  "message.send_on_behalf": "Sent on behalf",
  "folder.create": "Created folder",
  "folder.rename": "Renamed folder",
  "folder.delete": "Deleted folder",
  "folder.empty": "Emptied folder",
  "rules.update": "Changed rules",
  "auto_reply.update": "Changed automatic reply",
  "signatures.update": "Changed signatures",
}

const roleLabels: Record<AccessRole, string> = { owner: "Owner", delegate: "Delegate", member: "Member", admin: "Administrator" }

const clientLabels: Record<MailboxAccessEvent["client"], string> = { web: "Aether Mail", imap: "IMAP", app_password: "App password", token: "Access token", smtp: "SMTP" }

function detail(e: MailboxAccessEvent) {
  const to = typeof e.metadata.to === "string" ? ` → ${e.metadata.to}` : ""
  if (e.subject) return `${e.subject}${to}`
  if (typeof e.metadata.uid === "number") return `Message UID ${e.metadata.uid}${to}`
  return to.slice(3)
}

export function MailboxAccessReport({ mailboxId }: { mailboxId: string }) {
  const [since, setSince] = useState("")
  const [until, setUntil] = useState("")
  const report = useInfiniteQuery({
    queryKey: ["mailbox-access", mailboxId, since, until],
    queryFn: ({ pageParam }) => api<Report>(`/mailboxes/${mailboxId}/access-log`, { query: { since, until, before: pageParam } }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => (last.events.length === PAGE ? last.events[last.events.length - 1]?.id : undefined),
  })
  const summary = report.data?.pages[0]?.summary ?? []
  const events = report.data?.pages.flatMap((p) => p.events) ?? []

  return (
    <div className="flex flex-col gap-6">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field id={`access-since-${mailboxId}`} label="From">
          <Input id={`access-since-${mailboxId}`} type="date" max={until || undefined} value={since} onChange={(e) => setSince(e.target.value)} />
        </Field>
        <Field id={`access-until-${mailboxId}`} label="To">
          <Input id={`access-until-${mailboxId}`} type="date" min={since || undefined} value={until} onChange={(e) => setUntil(e.target.value)} />
        </Field>
      </div>
      {report.isPending ? (
        <RowsSkeleton rows={4} />
      ) : report.isError ? (
        <ErrorState error={report.error} onRetry={() => report.refetch()} />
      ) : events.length === 0 ? (
        <EmptyState icon={ShieldCheck} title="No access by others" description="Nobody other than the owner opened, changed or sent from this mailbox in this period, or access auditing was off." />
      ) : (
        <div className="animate-content flex flex-col gap-6">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Person</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Actions</TableHead>
                <TableHead>Last access</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {summary.map((s) => (
                <TableRow key={`${s.actorEmail}-${s.actorRole}`}>
                  <TableCell className="break-all">{s.actorEmail}</TableCell>
                  <TableCell>{roleLabels[s.actorRole]}</TableCell>
                  <TableCell>{plural(s.events, "action")}</TableCell>
                  <TableCell>{formatDateTime(s.last)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Person</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Folder</TableHead>
                <TableHead>Message</TableHead>
                <TableHead>Client</TableHead>
                <TableHead>IP address</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="whitespace-nowrap">{formatDateTime(e.createdAt)}</TableCell>
                  <TableCell className="break-all">{e.actorEmail}</TableCell>
                  <TableCell>{actionLabels[e.action] ?? e.action}</TableCell>
                  <TableCell className="font-mono text-sm">{e.folder || "—"}</TableCell>
                  <TableCell className="max-w-64 truncate" title={e.messageId || undefined}>
                    {detail(e) || "—"}
                  </TableCell>
                  <TableCell>{clientLabels[e.client]}</TableCell>
                  <TableCell className="font-mono text-sm">{e.ip || "—"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {report.hasNextPage && (
            <Button variant="secondary" className="self-center" loading={report.isFetchingNextPage} onClick={() => report.fetchNextPage()}>
              Load older entries
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

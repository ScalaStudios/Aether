"use client"

import { useInfiniteQuery } from "@tanstack/react-query"
import { ScrollText } from "lucide-react"

import { SectionHeader } from "@/components/page-header"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import type { AuditEvent } from "@/lib/types"

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
  )
}

"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ShieldBan, ShieldCheck } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDateTime, plural } from "@/lib/format"
import type { BlockedAddress } from "@/lib/types"

const protocolLabels: Record<string, string> = {
  imap: "IMAP",
  pop3: "POP3",
  smtp: "SMTP",
  submission: "SMTP",
  sieve: "ManageSieve",
  managesieve: "ManageSieve",
  web: "Web sign-in",
  dav: "CalDAV and CardDAV",
}

export function BlockedAddressesTab() {
  const qc = useQueryClient()
  const blocked = useQuery({
    queryKey: ["blocked-addresses"],
    queryFn: () => api<BlockedAddress[]>("/admin/blocked-addresses"),
    refetchInterval: 30_000,
  })
  const [unblocking, setUnblocking] = useState<BlockedAddress | null>(null)

  const unblock = useMutation({
    mutationFn: (id: string) => api(`/admin/blocked-addresses/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(`Unblocked ${unblocking?.address}`)
      setUnblocking(null)
      qc.invalidateQueries({ queryKey: ["blocked-addresses"] })
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Blocked addresses"
        description="Addresses that failed to sign in with too many different passwords within 10 minutes, on IMAP, SMTP, ManageSieve, calendar and contacts apps or the web. A block lasts 15 minutes and doubles for each repeat within a day, up to 24 hours."
      />
      <QueryView
        query={blocked}
        empty={<EmptyState icon={ShieldCheck} title="No blocked addresses" description="Addresses that keep failing to sign in are listed here while they are blocked." />}
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Last failure</TableHead>
                <TableHead>Failures</TableHead>
                <TableHead>Blocked until</TableHead>
                <TableHead className="w-16">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((b) => (
                <TableRow key={b.id}>
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <span className="font-mono text-sm">{b.address}</span>
                      {b.blocks > 1 && <span className="rounded-xs bg-muted px-2 py-1 text-xs">{`Blocked ${plural(b.blocks, "time")} today`}</span>}
                    </div>
                  </TableCell>
                  <TableCell>{protocolLabels[b.protocol] ?? b.protocol}</TableCell>
                  <TableCell className="tabular-nums">{b.failures}</TableCell>
                  <TableCell>{formatDateTime(b.blockedUntil)}</TableCell>
                  <TableCell>
                    <div className="flex justify-end">
                      <IconButton label={`Unblock ${b.address}`} size="icon-sm" onClick={() => setUnblocking(b)}>
                        <ShieldBan />
                      </IconButton>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <ConfirmDialog
        open={!!unblocking}
        onOpenChange={(open) => !open && setUnblocking(null)}
        title={`Unblock ${unblocking?.address}?`}
        description="Sign-ins from this address are accepted again straight away, and its count of failed sign-ins starts over."
        confirmLabel="Unblock"
        pending={unblock.isPending}
        onConfirm={() => unblocking && unblock.mutate(unblocking.id)}
      />
    </section>
  )
}

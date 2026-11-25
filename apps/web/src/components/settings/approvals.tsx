"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Inbox, X } from "lucide-react"
import { toast } from "sonner"

import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import type { FlowHeldMessage } from "@/lib/types"

export function FlowApprovals({ queryKey, path, hideWhenEmpty }: { queryKey: unknown[]; path: string; hideWhenEmpty?: boolean }) {
  const qc = useQueryClient()
  const held = useQuery({ queryKey, queryFn: () => api<FlowHeldMessage[]>(path) })
  const decide = useMutation({
    mutationFn: ({ id, approve }: { id: string; approve: boolean }) => api(`/mail-flow/held/${id}/${approve ? "approve" : "reject"}`, { method: "POST" }),
    onSuccess: (_, { approve }) => {
      toast.success(approve ? "Approved and sent on" : "Rejected; the message was discarded")
      qc.invalidateQueries({ queryKey })
    },
  })

  if (hideWhenEmpty && !held.isError && !held.data?.length) return null

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Messages held by mail flow rules"
        description="A mail flow rule holds these messages until someone approves them. Approved messages are delivered as they are; rejected and unanswered ones are discarded after 7 days without telling the sender."
      />
      <QueryView query={held} empty={<EmptyState icon={Inbox} title="Nothing is waiting" description="Messages held by a rule that sends for approval appear here." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Message</TableHead>
                <TableHead className="w-48">Rule</TableHead>
                <TableHead className="w-48">Received</TableHead>
                <TableHead className="w-56">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((m) => {
                const pending = decide.isPending && decide.variables?.id === m.id
                return (
                  <TableRow key={m.id}>
                    <TableCell className="whitespace-normal">
                      <p className="font-medium">{m.subject || "(no subject)"}</p>
                      <p className="text-sm text-fg-secondary">
                        From {m.sender || "an empty sender"} to {m.recipients.join(", ")}
                      </p>
                      {m.preview && <p className="mt-1 line-clamp-3 max-w-[72ch] text-sm whitespace-pre-line text-fg-tertiary">{m.preview}</p>}
                    </TableCell>
                    <TableCell className="whitespace-normal">{m.ruleName}</TableCell>
                    <TableCell>{formatDateTime(m.createdAt)}</TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-2">
                        <Button
                          size="sm"
                          variant="secondary"
                          disabled={decide.isPending}
                          loading={pending && decide.variables?.approve === false}
                          aria-label={`Reject ${m.subject}`}
                          onClick={() => decide.mutate({ id: m.id, approve: false })}
                        >
                          <X aria-hidden />
                          Reject
                        </Button>
                        <Button
                          size="sm"
                          disabled={decide.isPending}
                          loading={pending && decide.variables?.approve === true}
                          aria-label={`Approve ${m.subject}`}
                          onClick={() => decide.mutate({ id: m.id, approve: true })}
                        >
                          <Check aria-hidden />
                          Approve
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </QueryView>
    </section>
  )
}

"use client"

import { useQuery } from "@tanstack/react-query"
import { useState } from "react"

import { MailboxAccessReport } from "@/components/mailbox-access-report"
import { SectionHeader } from "@/components/page-header"
import { RowsSkeleton } from "@/components/states"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api, ApiError } from "@/lib/api"
import { myMailboxesQuery } from "@/lib/queries"

export function MailboxAccess() {
  const mailboxes = useQuery(myMailboxesQuery)
  const owned = (mailboxes.data ?? []).filter((m) => m.access === "owner")
  const [selected, setSelected] = useState("")
  const mailbox = owned.find((m) => m.id === selected) ?? owned[0]
  const allowed = useQuery({
    queryKey: ["mailbox-access-allowed", mailbox?.id],
    queryFn: () => api(`/mailboxes/${mailbox?.id}/access-log`, { query: { before: 1 } }),
    enabled: !!mailbox,
  })

  if (!mailbox || (allowed.error instanceof ApiError && allowed.error.status === 403)) return null

  return (
    <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <SectionHeader
        title="Who accessed your mailbox"
        description="Delegates, shared mailbox members and administrators who opened, changed or sent from your mailbox, as recorded by your organization."
        actions={
          owned.length > 1 && (
            <Select value={mailbox.id} onValueChange={setSelected}>
              <SelectTrigger aria-label="Mailbox" className="min-w-56">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {owned.map((m) => (
                  <SelectItem key={m.id} value={m.id}>
                    {m.address}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )
        }
      />
      {allowed.isPending ? <RowsSkeleton rows={3} /> : <MailboxAccessReport key={mailbox.id} mailboxId={mailbox.id} />}
    </section>
  )
}

"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Download } from "lucide-react"
import { useSearchParams } from "next/navigation"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { ErrorState, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/input"
import { api, apiUrl, errorMessage } from "@/lib/api"
import { formatBytes, formatDateTime } from "@/lib/format"
import type { HeldMessage } from "@/lib/types"

type Decision = "approve" | "reject"

export function HeldMessages() {
  const qc = useQueryClient()
  const params = useSearchParams()
  const held = useQuery({ queryKey: ["held-messages"], queryFn: () => api<HeldMessage[]>("/me/held-messages") })
  const [deciding, setDeciding] = useState<{ message: HeldMessage; decision: Decision } | null>(null)
  const [linkDismissed, setLinkDismissed] = useState(false)
  const [reason, setReason] = useState("")

  const items = held.data ?? []
  const linkId = linkDismissed ? null : params.get("held")
  const linked = linkId ? items.find((m) => m.id === linkId) : undefined
  const active = deciding ?? (linked ? { message: linked, decision: params.get("decision") === "reject" ? ("reject" as const) : ("approve" as const) } : null)
  const staleLink = !!linkId && held.isSuccess && !linked

  const decide = useMutation({
    mutationFn: ({ message, decision }: { message: HeldMessage; decision: Decision }) =>
      api<{ status?: string } | undefined>(`/held-messages/${message.id}/${decision}`, { method: "POST", body: decision === "reject" ? { reason: reason.trim() } : undefined }),
    meta: { silent: true },
    onSuccess: (data, { message, decision }) => {
      if (decision === "reject") toast.success("Rejected. The sender has been told.")
      else if (data?.status === "queued") toast.success(`Approved. Aether is still delivering it to ${message.listAddress} and will keep retrying.`)
      else toast.success(`Sent to ${message.listAddress}`)
      close()
      qc.invalidateQueries({ queryKey: ["held-messages"] })
    },
    onError: (e) => {
      toast.error(errorMessage(e))
      qc.invalidateQueries({ queryKey: ["held-messages"] })
    },
  })

  function open(message: HeldMessage, decision: Decision) {
    setReason("")
    setDeciding({ message, decision })
  }

  function close() {
    setDeciding(null)
    setLinkDismissed(true)
  }

  if (held.isSuccess && !items.length && !staleLink) return null

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Messages waiting for approval"
        description="Messages sent to lists you moderate. Approve one to deliver it to the members, or reject it to tell the sender. Messages nobody decides on within 5 days are discarded."
      />
      {staleLink && (
        <p role="status" className="rounded-lg border border-border bg-card px-4 py-3 text-sm text-fg-secondary">
          That message is no longer waiting for approval. Another moderator may have decided, or it expired.
        </p>
      )}
      {held.isPending ? (
        <RowsSkeleton />
      ) : held.isError ? (
        <ErrorState error={held.error} onRetry={() => held.refetch()} />
      ) : (
        <ul className="animate-content flex flex-col gap-4">
          {items.map((m) => (
            <li key={m.id} className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div className="flex min-w-0 flex-col gap-1">
                  <h3 className="truncate font-medium">{m.subject || "(no subject)"}</h3>
                  <p className="truncate text-sm text-fg-secondary">From {m.from || m.sender || "an unknown sender"}</p>
                </div>
                <StatusBadge tone="warning">{m.listName || m.listAddress}</StatusBadge>
              </div>
              {m.preview && <p className="line-clamp-4 text-sm whitespace-pre-line text-fg-secondary">{m.preview}</p>}
              <dl className="grid gap-2 text-sm sm:grid-cols-3">
                <div className="flex flex-col gap-1">
                  <dt className="text-label text-fg-tertiary">List</dt>
                  <dd className="truncate font-mono">{m.listAddress}</dd>
                </div>
                <div className="flex flex-col gap-1">
                  <dt className="text-label text-fg-tertiary">Received</dt>
                  <dd>{formatDateTime(m.receivedAt)}</dd>
                </div>
                <div className="flex flex-col gap-1">
                  <dt className="text-label text-fg-tertiary">Discarded</dt>
                  <dd>{formatDateTime(m.expiresAt)}</dd>
                </div>
              </dl>
              <div className="flex flex-wrap gap-2">
                <Button size="sm" onClick={() => open(m, "approve")}>
                  Approve
                </Button>
                <Button size="sm" variant="secondary" onClick={() => open(m, "reject")}>
                  Reject
                </Button>
                <Button size="sm" variant="quiet" asChild>
                  <a href={apiUrl(`/held-messages/${m.id}/raw`)} download>
                    <Download aria-hidden />
                    Download ({formatBytes(m.size)})
                  </a>
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}

      <FormDialog
        open={!!active}
        onOpenChange={(o) => !o && close()}
        title={active?.decision === "reject" ? "Reject this message?" : "Approve this message?"}
        description={
          active?.decision === "reject"
            ? `It is discarded, and ${active.message.sender || "the sender"} is told it was not delivered.`
            : `It goes to every member of ${active?.message.listAddress}.`
        }
        submitLabel={active?.decision === "reject" ? "Reject message" : "Approve and send"}
        pending={decide.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (active) decide.mutate(active)
        }}
      >
        <p className="text-sm">
          <span className="font-medium">{active?.message.subject || "(no subject)"}</span>
          <span className="text-fg-secondary"> from {active?.message.from || active?.message.sender}</span>
        </p>
        {active?.decision === "reject" && (
          <Field id="held-reason" label="Reason" hint="Optional. Sent to the sender.">
            <Textarea id="held-reason" rows={4} maxLength={2000} value={reason} onChange={(e) => setReason(e.target.value)} />
          </Field>
        )}
      </FormDialog>
    </section>
  )
}

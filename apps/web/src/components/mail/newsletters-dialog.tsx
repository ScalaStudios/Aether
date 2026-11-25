"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Newspaper } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { api, errorMessage } from "@/lib/api"
import { formatAddress } from "@/lib/format"
import { openUnsubscribeLink } from "@/lib/mail"
import type { NewsletterSender, UnsubscribeResult } from "@/lib/types"

export function NewslettersDialog({
  open,
  onOpenChange,
  mailboxId,
  readOnly,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  mailboxId: string
  readOnly?: boolean
}) {
  const qc = useQueryClient()
  const [done, setDone] = useState<Record<string, true>>({})
  const [block, setBlock] = useState<NewsletterSender | null>(null)
  const newsletters = useQuery({
    queryKey: ["newsletters", mailboxId],
    queryFn: () => api<{ senders: NewsletterSender[] }>(`/mailboxes/${mailboxId}/newsletters`),
    enabled: open && !!mailboxId,
  })
  const unsubscribe = useMutation({
    mutationFn: (sender: NewsletterSender) =>
      api<UnsubscribeResult>(`/mailboxes/${mailboxId}/messages/${sender.latestUid}/unsubscribe`, { method: "POST", query: { folder: sender.folder } }),
    onSuccess: (result, sender) => {
      if (result.url) {
        if (!openUnsubscribeLink(result.url)) toast.error("This unsubscribe link cannot be opened.")
        else toast.success("Opened the unsubscribe page.")
        return
      }
      setDone((current) => ({ ...current, [sender.address]: true }))
      toast.success(result.method === "mailto" ? "Unsubscribe request sent." : "Unsubscribed.")
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const blockSender = useMutation({
    mutationFn: (address: string) => api(`/mailboxes/${mailboxId}/newsletters/block`, { method: "POST", body: { address } }),
    onSuccess: () => {
      toast.success("Future messages from this sender will be deleted.")
      setBlock(null)
      qc.invalidateQueries({ queryKey: ["newsletters", mailboxId] })
      qc.invalidateQueries({ queryKey: ["rules", mailboxId] })
    },
    onError: (error) => toast.error(errorMessage(error)),
  })

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-[640px]">
          <DialogHeader>
            <DialogTitle>Newsletters</DialogTitle>
            <DialogDescription>Senders of the latest inbox messages that include an unsubscribe link.</DialogDescription>
          </DialogHeader>
          <QueryView
            query={newsletters}
            isEmpty={(data) => data.senders.length === 0}
            empty={<EmptyState icon={Newspaper} title="No newsletters" description="Recent inbox messages do not include an unsubscribe link." />}
          >
            {(data) => (
              <ul className="flex max-h-96 flex-col gap-3 overflow-y-auto">
                {data.senders.map((sender) => {
                  const mailtoOnly = sender.mailto && !sender.oneClick && !sender.link
                  const finished = done[sender.address]
                  return (
                    <li key={sender.address} className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border px-4 py-3">
                      <div className="min-w-0">
                        <p className="truncate font-semibold">{formatAddress({ name: sender.name, address: sender.address })}</p>
                        <p className="truncate text-sm text-fg-tertiary">
                          {sender.count} {sender.count === 1 ? "message" : "messages"}
                          {sender.subject ? ` · ${sender.subject}` : ""}
                        </p>
                      </div>
                      <div className="flex flex-wrap items-center gap-2">
                        {mailtoOnly && readOnly ? null : (
                          <Button
                            size="sm"
                            variant="secondary"
                            loading={unsubscribe.isPending && unsubscribe.variables?.address === sender.address}
                            disabled={finished || unsubscribe.isPending}
                            onClick={() => unsubscribe.mutate(sender)}
                          >
                            {finished ? "Unsubscribed" : "Unsubscribe"}
                          </Button>
                        )}
                        {sender.blocked ? (
                          <span className="text-sm text-fg-tertiary">Blocked</span>
                        ) : (
                          !readOnly && (
                            <Button size="sm" variant="secondary" disabled={blockSender.isPending} onClick={() => setBlock(sender)}>
                              Block
                            </Button>
                          )
                        )}
                      </div>
                    </li>
                  )
                })}
              </ul>
            )}
          </QueryView>
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={block !== null}
        onOpenChange={(next) => !next && setBlock(null)}
        title="Block this sender?"
        description={block ? `Future messages from ${block.address} will be deleted. You can change the mail rule under Settings.` : ""}
        confirmLabel="Block sender"
        pending={blockSender.isPending}
        onConfirm={() => block && blockSender.mutate(block.address)}
      />
    </>
  )
}

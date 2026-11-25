"use client"

import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ImageOff, ShieldCheck } from "lucide-react"

import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import type { Message } from "@/lib/types"

type Action = "show" | "hide" | "allowSender" | "forgetSender"

function trackerCopy(count: number) {
  if (count <= 0) return ""
  return count === 1 ? "1 tracking image was removed and was not loaded." : `${count} tracking images were removed and were not loaded.`
}

export function RemoteImagesNotice({ message, mailboxId, folder }: { message: Message; mailboxId: string; folder: string }) {
  const images = message.remoteImages
  const qc = useQueryClient()
  const choose = useMutation({
    mutationFn: (action: Action) =>
      api<Message>(`/mailboxes/${mailboxId}/messages/${message.uid}/remote-images`, { method: "POST", query: { folder }, body: { action } }),
    onSuccess: (updated, action) => {
      qc.setQueryData(["message", mailboxId, folder, message.uid], updated)
      if (action === "allowSender" || action === "forgetSender") qc.invalidateQueries({ queryKey: ["remote-image-senders"] })
    },
  })
  if (!images || images.status === "none") return null

  const sender = images.sender
  const shown = images.status === "proxied"
  const title = shown ? "Images are shown through Aether" : images.remote > 0 ? "Remote images are hidden" : "Tracking images were removed"
  const detail = shown
    ? "Aether fetched them, so the sender sees Aether's address, not your IP address or browser."
    : images.remote > 0
      ? "Aether has not contacted the sender, so they cannot tell that you opened this message."
      : "Aether did not load them."
  const trackers = trackerCopy(images.trackers)
  const pending = (action: Action) => choose.isPending && choose.variables === action

  return (
    <section aria-label="Remote images" aria-busy={choose.isPending || undefined} className="flex flex-col gap-3 rounded-xl border border-border bg-muted px-4 py-3">
      <div className="flex items-start gap-3">
        {shown ? <ShieldCheck aria-hidden className="mt-0.5 size-5 shrink-0 text-success" /> : <ImageOff aria-hidden className="mt-0.5 size-5 shrink-0 text-fg-tertiary" />}
        <div className="flex min-w-0 flex-col gap-1">
          <h3 className="font-semibold">{title}</h3>
          <p className="text-sm text-pretty text-fg-secondary">
            {detail} {trackers}
            {sender && images.senderAllowed ? ` Images from ${sender} always load.` : ""}
            {shown && images.policy === "proxy" && !images.senderAllowed && !images.messageAllowed ? " Your organization loads remote images this way." : ""}
          </p>
        </div>
      </div>
      {(images.remote > 0 || images.senderAllowed) && (
        <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap">
          {!shown && images.remote > 0 && (
            <Button size="sm" className="max-md:min-h-11" loading={pending("show")} onClick={() => choose.mutate("show")}>
              Show images
            </Button>
          )}
          {shown && images.messageAllowed && !images.senderAllowed && images.policy === "block" && (
            <Button size="sm" variant="secondary" className="max-md:min-h-11" loading={pending("hide")} onClick={() => choose.mutate("hide")}>
              Hide images
            </Button>
          )}
          {sender && !images.senderAllowed && (
            <Button size="sm" variant="secondary" className="max-md:min-h-11" loading={pending("allowSender")} onClick={() => choose.mutate("allowSender")}>
              Always show from this sender
            </Button>
          )}
          {sender && images.senderAllowed && (
            <Button size="sm" variant="secondary" className="max-md:min-h-11" loading={pending("forgetSender")} onClick={() => choose.mutate("forgetSender")}>
              Stop always showing
            </Button>
          )}
        </div>
      )}
    </section>
  )
}

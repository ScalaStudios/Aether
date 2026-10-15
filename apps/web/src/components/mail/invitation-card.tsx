"use client"

import { useMutation, useQueryClient } from "@tanstack/react-query"
import { CalendarDays } from "lucide-react"
import { useEffect, useRef } from "react"
import { toast } from "sonner"

import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { api, errorMessage } from "@/lib/api"
import { attendeeStatusLabels, formatAddress } from "@/lib/format"
import type { Invitation, Message } from "@/lib/types"

const dateTimeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "full", timeStyle: "short" })
const dateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "full" })

const responses = [
  { value: "accepted", label: "Going" },
  { value: "tentative", label: "Maybe" },
  { value: "declined", label: "Not going" },
] as const

function when({ event }: Invitation) {
  const start = new Date(event.start)
  const end = new Date(event.end)
  if (event.allDay) return dateFormat.formatRange(start, new Date(end.getTime() - 86_400_000))
  return dateTimeFormat.formatRange(start, end)
}

export function InvitationCard({ message, mailboxId, folder }: { message: Message; mailboxId: string; folder: string }) {
  const qc = useQueryClient()
  const invitation = message.invitation!
  const respond = useMutation({
    mutationFn: (response: string) =>
      api<Invitation>(`/mailboxes/${mailboxId}/messages/${message.uid}/invitation`, { method: "POST", query: { folder }, body: { response } }),
    onSuccess: (updated) => {
      qc.setQueryData<Message>(["message", mailboxId, folder, message.uid], (m) => m && { ...m, invitation: updated })
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const applying = useRef(false)
  const pendingReply = invitation.method === "reply" && !!invitation.eventId && !invitation.applied

  useEffect(() => {
    if (pendingReply && !applying.current) {
      applying.current = true
      respond.mutate("apply")
    }
  }, [pendingReply, respond])

  const sender = message.from[0] ? message.from[0].name || message.from[0].address : "The attendee"
  const status = invitation.status || "needs-action"

  return (
    <section aria-label="Calendar invitation" className="flex flex-col gap-3 rounded-xl border border-border p-4">
      <div className="flex items-start gap-3">
        <CalendarDays aria-hidden className="mt-1 size-5 shrink-0 text-fg-tertiary" />
        <div className="flex min-w-0 flex-col gap-1">
          <p className="font-semibold break-words">{invitation.event.title || "(untitled event)"}</p>
          <p className="text-sm text-fg-secondary">{when(invitation)}</p>
          {invitation.event.location && <p className="text-sm text-fg-secondary break-words">{invitation.event.location}</p>}
          {invitation.organizer.address && <p className="text-sm text-fg-tertiary break-all">Organizer: {formatAddress(invitation.organizer)}</p>}
        </div>
      </div>
      {invitation.method === "request" && (
        <div role="group" aria-label="Your response" className="flex flex-wrap items-center gap-2">
          {responses.map((r) => (
            <Button
              key={r.value}
              size="sm"
              variant={status === r.value ? "default" : "secondary"}
              aria-pressed={status === r.value}
              loading={respond.isPending && respond.variables === r.value}
              disabled={respond.isPending}
              onClick={() => respond.mutate(r.value)}
            >
              {r.label}
            </Button>
          ))}
          {invitation.eventId && <span className="text-sm text-fg-tertiary">In your calendar</span>}
        </div>
      )}
      {invitation.method === "reply" && (
        <p className="flex flex-wrap items-center gap-2 text-sm">
          {sender} replied <StatusBadge tone={status === "accepted" ? "success" : status === "declined" ? "danger" : "warning"}>{attendeeStatusLabels[status]}</StatusBadge>
          <span className="text-fg-tertiary">{invitation.applied ? "Your event is up to date." : invitation.eventId ? "Updating your event…" : "The event is no longer in your calendar."}</span>
        </p>
      )}
      {invitation.method === "cancel" && (
        <div className="flex flex-wrap items-center gap-3">
          <StatusBadge tone="danger">Cancelled</StatusBadge>
          {invitation.eventId ? (
            <Button size="sm" variant="secondary" loading={respond.isPending} onClick={() => respond.mutate("remove")}>
              Remove from calendar
            </Button>
          ) : (
            <span className="text-sm text-fg-tertiary">Not in your calendar</span>
          )}
        </div>
      )}
    </section>
  )
}

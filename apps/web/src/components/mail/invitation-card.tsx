"use client"

import { useMutation, useQueryClient } from "@tanstack/react-query"
import { CalendarDays } from "lucide-react"
import { useEffect, useMemo, useRef } from "react"
import { toast } from "sonner"

import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { api, errorMessage } from "@/lib/api"
import { attendeeStatusLabel, formatAddress } from "@/lib/format"
import { useI18n } from "@/lib/i18n"
import type { AttendeeStatus, Invitation, Message } from "@/lib/types"

const responseValues: readonly AttendeeStatus[] = ["accepted", "tentative", "declined"]

export function InvitationCard({ message, mailboxId, folder }: { message: Message; mailboxId: string; folder: string }) {
  const { t, locale } = useI18n()
  const qc = useQueryClient()
  const invitation = message.invitation!
  const formatters = useMemo(
    () => ({
      dateTime: new Intl.DateTimeFormat(locale, { dateStyle: "full", timeStyle: "short" }),
      date: new Intl.DateTimeFormat(locale, { dateStyle: "full" }),
    }),
    [locale]
  )
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

  const sender = message.from[0] ? message.from[0].name || message.from[0].address : t("mail.invitation.theAttendee")
  const status = invitation.status || "needs-action"
  const start = new Date(invitation.event.start)
  const end = new Date(invitation.event.end)
  const whenText = invitation.event.allDay
    ? formatters.date.formatRange(start, new Date(end.getTime() - 86_400_000))
    : formatters.dateTime.formatRange(start, end)

  return (
    <section aria-label={t("mail.invitation.sectionLabel")} className="flex flex-col gap-3 rounded-xl border border-border p-4">
      <div className="flex items-start gap-3">
        <CalendarDays aria-hidden className="mt-1 size-5 shrink-0 text-fg-tertiary" />
        <div className="flex min-w-0 flex-col gap-1">
          <p className="font-semibold break-words">{invitation.event.title || t("mail.invitation.untitled")}</p>
          <p className="text-sm text-fg-secondary">{whenText}</p>
          {invitation.event.location && <p className="text-sm text-fg-secondary break-words">{invitation.event.location}</p>}
          {invitation.organizer.address && (
            <p className="text-sm text-fg-tertiary break-all">{t("mail.invitation.organizer", { address: formatAddress(invitation.organizer) })}</p>
          )}
        </div>
      </div>
      {invitation.method === "request" && (
        <div role="group" aria-label={t("mail.invitation.yourResponse")} className="flex flex-wrap items-center gap-2">
          {responseValues.map((value) => (
            <Button
              key={value}
              size="sm"
              variant={status === value ? "default" : "secondary"}
              aria-pressed={status === value}
              loading={respond.isPending && respond.variables === value}
              disabled={respond.isPending}
              onClick={() => respond.mutate(value)}
            >
              {attendeeStatusLabel(value)}
            </Button>
          ))}
          {invitation.eventId && <span className="text-sm text-fg-tertiary">{t("mail.invitation.inYourCalendar")}</span>}
        </div>
      )}
      {invitation.method === "reply" && (
        <p className="flex flex-wrap items-center gap-2 text-sm">
          {t("mail.invitation.replied", { sender })}{" "}
          <StatusBadge tone={status === "accepted" ? "success" : status === "declined" ? "danger" : "warning"}>{attendeeStatusLabel(status)}</StatusBadge>
          <span className="text-fg-tertiary">
            {invitation.applied
              ? t("mail.invitation.eventUpToDate")
              : invitation.eventId
                ? t("mail.invitation.updatingEvent")
                : t("mail.invitation.eventRemoved")}
          </span>
        </p>
      )}
      {invitation.method === "cancel" && (
        <div className="flex flex-wrap items-center gap-3">
          <StatusBadge tone="danger">{t("mail.invitation.cancelled")}</StatusBadge>
          {invitation.eventId ? (
            <Button size="sm" variant="secondary" loading={respond.isPending} onClick={() => respond.mutate("remove")}>
              {t("mail.invitation.removeFromCalendar")}
            </Button>
          ) : (
            <span className="text-sm text-fg-tertiary">{t("mail.invitation.notInCalendar")}</span>
          )}
        </div>
      )}
    </section>
  )
}

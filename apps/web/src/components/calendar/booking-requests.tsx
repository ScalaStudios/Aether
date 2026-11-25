"use client"

import { useMutation, useQueryClient, type UseQueryResult } from "@tanstack/react-query"
import { CalendarCheck, TriangleAlert } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Textarea } from "@/components/ui/input"
import { api, errorMessage } from "@/lib/api"
import { formatAddress } from "@/lib/format"
import { resourceTypes } from "@/lib/resources"
import type { BookingRequest } from "@/lib/types"

const dayFormat = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short", year: "numeric" })
const timeFormat = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })
const repeatLabels: Record<string, string> = { daily: "Repeats daily", weekly: "Repeats weekly", monthly: "Repeats monthly", yearly: "Repeats yearly", custom: "Repeats" }

function when(r: BookingRequest) {
  const start = new Date(r.start)
  const end = new Date(r.end)
  if (r.allDay) return dayFormat.formatRange(start, new Date(end.getTime() - 1))
  return timeFormat.formatRange(start, end)
}

export function BookingRequestsDialog({ open, onOpenChange, requests }: { open: boolean; onOpenChange: (open: boolean) => void; requests: UseQueryResult<BookingRequest[]> }) {
  const qc = useQueryClient()
  const [declining, setDeclining] = useState<BookingRequest | null>(null)
  const [comment, setComment] = useState("")

  const decide = useMutation({
    mutationFn: ({ request, decision }: { request: BookingRequest; decision: "approve" | "decline" }) =>
      api(`/calendar/booking-requests/${request.id}`, { method: "POST", body: { decision, comment: decision === "decline" ? comment : "" } }),
    meta: { silent: true },
    onSuccess: (_d, { request, decision }) => {
      toast.success(decision === "approve" ? `Booked ${request.resource.name}` : `Declined the request for ${request.resource.name}`, {
        description: `${request.organizer.name || request.organizer.address} was told by email.`,
      })
      setDeclining(null)
      setComment("")
    },
    onError: (error) => toast.error(errorMessage(error)),
    onSettled: () => qc.invalidateQueries({ queryKey: ["booking-requests"] }),
  })
  const pending = (r: BookingRequest) => decide.isPending && decide.variables.request.id === r.id

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-[640px]">
          <DialogHeader>
            <DialogTitle>Booking requests</DialogTitle>
            <DialogDescription>Rooms and equipment you approve. The organizer gets an email with your decision.</DialogDescription>
          </DialogHeader>
          <QueryView
            query={requests}
            empty={<EmptyState icon={CalendarCheck} title="Nothing waiting" description="New requests for rooms and equipment you approve show up here, and you get an email for each one." className="py-8" />}
          >
            {(list) => (
              <ul className="flex flex-col gap-3">
                {list.map((r) => {
                  const Icon = resourceTypes[r.resource.type].icon
                  return (
                    <li key={r.id} className="flex flex-col gap-3 rounded-xl border border-border bg-sunken p-4">
                      <div className="flex items-start gap-3">
                        <Icon aria-hidden className="mt-1 size-4 shrink-0 text-fg-tertiary" />
                        <div className="flex min-w-0 flex-1 flex-col gap-1">
                          <span className="text-sm text-fg-tertiary">{r.resource.name}</span>
                          <span className="font-semibold break-words">{r.title || "Untitled event"}</span>
                          <span className="text-sm text-fg-secondary">{[when(r), r.repeat && repeatLabels[r.repeat]].filter(Boolean).join(" · ")}</span>
                          <span className="text-sm break-all text-fg-tertiary">Requested by {formatAddress(r.organizer)}</span>
                        </div>
                      </div>
                      {r.conflict && (
                        <p className="flex items-start gap-2 text-sm text-warning">
                          <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
                          {r.conflict}
                        </p>
                      )}
                      <div className="flex flex-wrap justify-end gap-2">
                        <Button variant="secondary" size="sm" className="max-md:min-h-11" disabled={pending(r)} onClick={() => setDeclining(r)}>
                          Decline
                        </Button>
                        <Button size="sm" className="max-md:min-h-11" disabled={!!r.conflict} loading={pending(r) && decide.variables?.decision === "approve"} onClick={() => decide.mutate({ request: r, decision: "approve" })}>
                          Approve
                        </Button>
                      </div>
                    </li>
                  )
                })}
              </ul>
            )}
          </QueryView>
        </DialogContent>
      </Dialog>

      <FormDialog
        open={!!declining}
        onOpenChange={(o) => {
          if (!o) {
            setDeclining(null)
            setComment("")
          }
        }}
        title={`Decline “${declining?.title ?? ""}”?`}
        description={declining ? `${declining.organizer.name || declining.organizer.address} is told that ${declining.resource.name} isn’t booked.` : undefined}
        submitLabel="Decline request"
        pending={decide.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (declining) decide.mutate({ request: declining, decision: "decline" })
        }}
      >
        <Field id="decline-comment" label="Message to the organizer" hint="Optional. For example, suggest another room or time.">
          <Textarea id="decline-comment" rows={3} maxLength={500} className="min-h-24" value={comment} onChange={(e) => setComment(e.target.value)} />
        </Field>
      </FormDialog>
    </>
  )
}

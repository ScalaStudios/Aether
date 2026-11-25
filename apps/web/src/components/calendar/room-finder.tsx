"use client"

import type { UseQueryResult } from "@tanstack/react-query"
import { Plus, SearchX, X } from "lucide-react"
import { useState } from "react"
import { cn } from "cn"

import { IconButton } from "@/components/icon-button"
import { SearchInput } from "@/components/search-input"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { availabilityStatus, bookingStatus, resourceDetails, resourceTypes, ruleSummary } from "@/lib/resources"
import type { Address, AttendeeStatus, ResourceChoice, ResourceType } from "@/lib/types"

type Room = Address & { kind?: ResourceType }

const whenFormat = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })

export interface RoomRange {
  start: string
  end: string
}

function blocked(choice?: ResourceChoice) {
  return !!choice && (!choice.bookable || choice.availability === "busy" || choice.availability === "restricted")
}

export function RoomField({
  rooms,
  choices,
  range,
  savedStatus,
  onChange,
}: {
  rooms: Room[]
  choices: UseQueryResult<ResourceChoice[]>
  range: RoomRange | null
  savedStatus: (address: string) => AttendeeStatus | undefined
  onChange: (rooms: Room[]) => void
}) {
  const [finding, setFinding] = useState(false)
  if (rooms.length === 0 && !choices.data?.length) return null
  const choiceFor = (address: string) => choices.data?.find((c) => c.address === address.toLowerCase())

  return (
    <div role="group" aria-labelledby="event-rooms-label" className="flex flex-col gap-2">
      <Label asChild>
        <span id="event-rooms-label">Rooms and equipment</span>
      </Label>
      {rooms.length > 0 && (
        <ul className="flex flex-col gap-2">
          {rooms.map((room) => {
            const choice = choiceFor(room.address)
            const saved = savedStatus(room.address)
            const status = saved ? bookingStatus[saved] : choice?.availability ? availabilityStatus[choice.availability] : null
            const Icon = resourceTypes[choice?.type ?? room.kind ?? "room"].icon
            const name = choice?.name || room.name || room.address
            const details = choice ? resourceDetails(choice) : ""
            return (
              <li key={room.address} className="flex items-start gap-3 rounded-lg border border-border bg-sunken px-3 py-2">
                <Icon aria-hidden className="mt-1 size-4 shrink-0 text-fg-tertiary" />
                <div className="flex min-w-0 flex-1 flex-col gap-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-semibold break-words">{name}</span>
                    {status && <StatusBadge tone={status.tone}>{status.label}</StatusBadge>}
                  </div>
                  {details && <span className="text-sm text-fg-tertiary">{details}</span>}
                  {!saved && choice?.reason && <span className="text-sm text-warning">{choice.reason}</span>}
                  {!saved && !choice?.reason && choice?.policy === "approval" && <span className="text-sm text-fg-tertiary">Needs approval after you save.</span>}
                </div>
                <IconButton type="button" label={`Remove ${name}`} size="icon-sm" className="-my-1" onClick={() => onChange(rooms.filter((r) => r !== room))}>
                  <X />
                </IconButton>
              </li>
            )
          })}
        </ul>
      )}
      <Button type="button" variant="secondary" size="sm" className="self-start max-md:min-h-11" onClick={() => setFinding(true)}>
        <Plus aria-hidden />
        Add room or equipment
      </Button>
      <RoomFinder
        open={finding}
        onOpenChange={setFinding}
        choices={choices}
        range={range}
        chosen={rooms.map((r) => r.address.toLowerCase())}
        onAdd={(c) => {
          onChange([...rooms, { name: c.name, address: c.address, kind: c.type }])
          setFinding(false)
        }}
      />
    </div>
  )
}

function RoomFinder({
  open,
  onOpenChange,
  choices,
  range,
  chosen,
  onAdd,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  choices: UseQueryResult<ResourceChoice[]>
  range: RoomRange | null
  chosen: string[]
  onAdd: (choice: ResourceChoice) => void
}) {
  const [search, setSearch] = useState("")
  const [type, setType] = useState<ResourceType | "all">("all")
  const types = new Set(choices.data?.map((c) => c.type))
  const term = search.trim().toLowerCase()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[640px]">
        <DialogHeader>
          <DialogTitle>Add room or equipment</DialogTitle>
          <DialogDescription>
            {range ? `Availability for ${whenFormat.formatRange(new Date(range.start), new Date(range.end))}.` : "Choose when the event starts and ends to see what’s available."}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
          <SearchInput aria-label="Search rooms and equipment" placeholder="Search by name or location" value={search} onChange={(e) => setSearch(e.target.value)} className="flex-1" />
          {types.size > 1 && (
            <SegmentedControl
              label="Type"
              options={[{ value: "all" as const, label: "All" }, ...(["room", "equipment"] as const).map((t) => ({ value: t, label: resourceTypes[t].plural }))]}
              value={type}
              onChange={setType}
            />
          )}
        </div>
        <QueryView query={choices} skeleton={<RowsSkeleton rows={3} />}>
          {(list) => {
            const shown = list.filter((c) => (type === "all" || c.type === type) && (!term || [c.name, c.location, c.address].some((v) => v.toLowerCase().includes(term))))
            if (shown.length === 0) return <EmptyState icon={SearchX} title="Nothing matches" description={term ? `No room or equipment matches “${search.trim()}”.` : "Try another type."} className="py-8" />
            return (
              <ul aria-busy={choices.isFetching} className={cn("flex flex-col divide-y divide-border transition-opacity duration-160", choices.isPlaceholderData && "opacity-60")}>
                {shown.map((c) => {
                  const Icon = resourceTypes[c.type].icon
                  const added = chosen.includes(c.address)
                  const status = c.availability ? availabilityStatus[c.availability] : null
                  const details = [resourceDetails(c), ...ruleSummary(c)].filter(Boolean).join(" · ")
                  return (
                    <li key={c.id} className="flex items-start gap-3 py-3">
                      <Icon aria-hidden className="mt-1 size-4 shrink-0 text-fg-tertiary" />
                      <div className="flex min-w-0 flex-1 flex-col gap-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-semibold">{c.name}</span>
                          {status && <StatusBadge tone={status.tone}>{status.label}</StatusBadge>}
                        </div>
                        {details && <span className="text-sm text-fg-tertiary">{details}</span>}
                        {c.description && <span className="line-clamp-2 text-sm text-fg-secondary">{c.description}</span>}
                        {c.reason && <span className="text-sm text-warning">{c.reason}</span>}
                      </div>
                      <Button type="button" size="sm" variant="secondary" disabled={added || blocked(c)} onClick={() => onAdd(c)} aria-label={added ? `${c.name} added` : `Add ${c.name}`}>
                        {added ? "Added" : "Add"}
                      </Button>
                    </li>
                  )
                })}
              </ul>
            )
          }}
        </QueryView>
      </DialogContent>
    </Dialog>
  )
}

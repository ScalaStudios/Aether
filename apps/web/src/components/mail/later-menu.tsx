"use client"

import { useState } from "react"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { addDays, dateValue, fromValues, startOfDay, startOfWeek } from "@/lib/calendar"
import { formatWhen } from "@/lib/format"

function presets(laterToday: boolean) {
  const now = new Date()
  const tomorrow = addDays(startOfDay(now), 1)
  return [
    ...(laterToday ? [{ label: "Later today", at: new Date(now.getTime() + 3 * 3_600_000) }] : []),
    { label: "Tomorrow morning", at: new Date(tomorrow.setHours(8)) },
    { label: "Monday morning", at: new Date(addDays(startOfWeek(now), 7).setHours(8)) },
  ]
}

export function LaterMenu({
  label,
  submitLabel,
  laterToday = false,
  disabled,
  onPick,
  children,
}: {
  label: string
  submitLabel: string
  laterToday?: boolean
  disabled?: boolean
  onPick: (at: Date) => void
  children: React.ReactNode
}) {
  const [picking, setPicking] = useState(false)
  const [date, setDate] = useState("")
  const [time, setTime] = useState("08:00")
  const [error, setError] = useState("")

  function openPicker() {
    setDate(dateValue(addDays(new Date(), 1)))
    setTime("08:00")
    setError("")
    setPicking(true)
  }

  return (
    <>
      <DropdownMenu>
        <Tooltip>
          <TooltipTrigger asChild>
            <DropdownMenuTrigger asChild disabled={disabled}>
              {children}
            </DropdownMenuTrigger>
          </TooltipTrigger>
          <TooltipContent>{label}</TooltipContent>
        </Tooltip>
        <DropdownMenuContent align="end" className="w-auto min-w-64">
          <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">{label}</DropdownMenuLabel>
          {presets(laterToday).map((p) => (
            <DropdownMenuItem key={p.label} onSelect={() => onPick(p.at)}>
              <span className="flex-1 whitespace-nowrap">{p.label}</span>
              <span className="pl-4 text-sm whitespace-nowrap text-fg-tertiary tabular-nums">{formatWhen(p.at)}</span>
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={openPicker}>Pick date and time…</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <FormDialog
        open={picking}
        onOpenChange={setPicking}
        title={label}
        submitLabel={submitLabel}
        onSubmit={(e) => {
          e.preventDefault()
          e.stopPropagation()
          const at = date && time ? fromValues(date, time) : null
          if (!at || at <= new Date()) {
            setError("Choose a time in the future.")
            return
          }
          setPicking(false)
          onPick(at)
        }}
      >
        <Field id="later-date" label="Date and time" error={error}>
          <div className="flex gap-2">
            <Input id="later-date" type="date" value={date} aria-invalid={!!error} onChange={(e) => setDate(e.target.value)} />
            <Input type="time" aria-label="Time" value={time} aria-invalid={!!error} className="w-32 shrink-0" onChange={(e) => setTime(e.target.value)} />
          </div>
        </Field>
      </FormDialog>
    </>
  )
}

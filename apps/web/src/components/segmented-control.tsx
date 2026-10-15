"use client"

import { cn } from "cn"

import { Button } from "@/components/ui/button"

export function SegmentedControl<T extends string | number>({
  label,
  options,
  value,
  onChange,
}: {
  label: string
  options: { value: T; label: string }[]
  value: T
  onChange: (value: T) => void
}) {
  return (
    <div role="group" aria-label={label} className="flex gap-1 rounded-lg border border-strong p-1">
      {options.map((o) => (
        <Button
          key={o.value}
          type="button"
          variant="quiet"
          size="sm"
          aria-pressed={value === o.value}
          className={cn("max-md:min-h-11", value === o.value && "bg-muted text-foreground")}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </Button>
      ))}
    </div>
  )
}

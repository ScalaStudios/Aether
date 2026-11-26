"use client"

import { cn } from "@/lib/utils"

import { Button } from "@/components/ui/button"

export function SegmentedControl<T extends string | number>({
  label,
  options,
  value,
  onChange,
  className,
}: {
  label: string
  options: { value: T; label: React.ReactNode }[]
  value: T | T[]
  onChange: (value: T) => void
  className?: string
}) {
  const pressed = (v: T) => (Array.isArray(value) ? value.includes(v) : value === v)
  return (
    <div role="group" aria-label={label} className={cn("flex max-w-full flex-wrap gap-1 rounded-lg border border-strong p-1", className)}>
      {options.map((o) => (
        <Button
          key={o.value}
          type="button"
          variant="quiet"
          size="sm"
          aria-pressed={pressed(o.value)}
          className={cn("max-md:min-h-11", pressed(o.value) && "bg-muted text-foreground")}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </Button>
      ))}
    </div>
  )
}

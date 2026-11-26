"use client"

import { ChevronDown } from "lucide-react"
import { cn } from "@/lib/utils"

import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"

export function MultiSelect({
  id,
  options,
  value,
  onChange,
  placeholder,
  invalid,
}: {
  id: string
  options: { value: string; label: string; hint?: string }[]
  value: string[]
  onChange: (value: string[]) => void
  placeholder: string
  invalid?: boolean
}) {
  const selected = options.filter((o) => value.includes(o.value))
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        id={id}
        disabled={!options.length}
        aria-invalid={invalid}
        className="flex h-10 w-full cursor-pointer items-center justify-between gap-2 rounded-lg border border-input bg-sunken py-2 pr-3 pl-4 text-left text-base transition-colors outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive max-md:h-11"
      >
        <span className={cn("truncate", !selected.length && "text-muted-foreground")}>{selected.length ? selected.map((o) => o.label).join(", ") : placeholder}</span>
        <ChevronDown aria-hidden className="size-4 shrink-0 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent>
        {options.map((o) => (
          <DropdownMenuCheckboxItem
            key={o.value}
            checked={value.includes(o.value)}
            onSelect={(e) => e.preventDefault()}
            onCheckedChange={(checked) => onChange(checked ? [...value, o.value] : value.filter((v) => v !== o.value))}
          >
            <span className="flex min-w-0 flex-col">
              <span className="truncate">{o.label}</span>
              {o.hint && <span className="truncate text-sm text-fg-tertiary">{o.hint}</span>}
            </span>
          </DropdownMenuCheckboxItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

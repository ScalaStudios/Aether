"use client"

import { useState } from "react"

import { SearchInput } from "@/components/search-input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

export function SelectionList({
  id,
  label,
  items,
  value,
  onChange,
}: {
  id: string
  label: string
  items: { id: string; label: string; hint?: string }[]
  value: string[]
  onChange: (value: string[]) => void
}) {
  const [filter, setFilter] = useState("")
  const term = filter.trim().toLowerCase()
  const shown = term ? items.filter((i) => i.label.toLowerCase().includes(term)) : items

  return (
    <div className="flex flex-col gap-2">
      <SearchInput aria-label={`Filter ${label.toLowerCase()}`} placeholder={`Filter ${label.toLowerCase()}`} value={filter} onChange={(e) => setFilter(e.target.value)} />
      <ul aria-label={label} className="flex max-h-64 flex-col overflow-y-auto rounded-lg border border-strong">
        {shown.length === 0 ? (
          <li className="px-4 py-3 text-sm text-fg-tertiary">Nothing matches.</li>
        ) : (
          shown.map((item) => (
            <li key={item.id} className="flex items-center gap-3 border-b border-border px-4 py-2 last:border-b-0">
              <Switch
                id={`${id}-${item.id}`}
                checked={value.includes(item.id)}
                onCheckedChange={(on) => onChange(on ? [...value, item.id] : value.filter((v) => v !== item.id))}
              />
              <Label htmlFor={`${id}-${item.id}`} className="flex min-w-0 flex-1 flex-col items-start gap-0">
                <span className="truncate font-mono text-sm">{item.label}</span>
                {item.hint && <span className="text-xs font-normal text-fg-tertiary">{item.hint}</span>}
              </Label>
            </li>
          ))
        )}
      </ul>
      <p className="text-sm text-fg-tertiary">{value.length} selected</p>
    </div>
  )
}

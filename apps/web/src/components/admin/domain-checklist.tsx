"use client"

import { cn } from "cn"

import type { Domain } from "@/lib/types"

export function DomainChecklist({
  domains,
  value,
  onChange,
  loading,
  label = "Domains",
  hint,
  error,
  empty = "No domains in this organization yet.",
}: {
  domains: Domain[] | undefined
  value: string[]
  onChange: (ids: string[]) => void
  loading?: boolean
  label?: string
  hint?: string
  error?: string
  empty?: string
}) {
  const selected = new Set(value)

  function toggle(id: string) {
    if (selected.has(id)) {
      onChange(value.filter((v) => v !== id))
    } else {
      onChange([...value, id])
    }
  }

  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="text-label text-fg-secondary">{label}</legend>
      {hint && <p className="text-sm text-fg-tertiary">{hint}</p>}
      {loading ? (
        <p className="text-sm text-fg-tertiary">Loading domains…</p>
      ) : !domains || domains.length === 0 ? (
        <p className="rounded-lg border border-border bg-sunken px-3 py-2 text-sm text-fg-tertiary">{empty}</p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {domains.map((d) => {
            const checked = selected.has(d.id)
            return (
              <label
                key={d.id}
                className={cn(
                  "flex min-h-10 cursor-pointer items-center gap-2 rounded-lg border px-3 py-1.5 text-sm transition-colors select-none",
                  checked ? "border-ember/60 bg-ember/10 text-foreground" : "border-border bg-sunken text-fg-secondary hover:border-strong"
                )}
              >
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={() => toggle(d.id)}
                  className="size-4 accent-ember"
                />
                <span className="font-mono">{d.name}</span>
              </label>
            )
          })}
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
    </fieldset>
  )
}

"use client"

import { useQuery } from "@tanstack/react-query"
import { X } from "lucide-react"
import { useState } from "react"
import { z } from "zod"

import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { api } from "@/lib/api"
import { formatAddress } from "@/lib/format"
import type { Address, Recipient } from "@/lib/types"

const emailSchema = z.email()

function parse(raw: string): Address | null {
  const match = raw.match(/^\s*"?([^"<]*?)"?\s*<([^>]+)>\s*$/)
  const address = (match ? match[2] : raw).trim()
  if (!emailSchema.safeParse(address).success) return null
  const name = match?.[1].trim()
  return name ? { name, address } : { address }
}

export function RecipientInput({
  id,
  value,
  onChange,
  invalid,
  placeholder,
  suggest,
}: {
  id: string
  value: Address[]
  onChange: (value: Address[]) => void
  invalid?: boolean
  placeholder?: string
  suggest?: boolean
}) {
  const [draft, setDraft] = useState("")
  const [error, setError] = useState("")
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const term = useDebouncedValue(draft.trim(), 150)
  const suggestions = useQuery({
    queryKey: ["recipients", term],
    queryFn: () => api<Recipient[]>("/recipients", { query: { q: term } }),
    enabled: !!suggest && term.length >= 2,
    staleTime: 30_000,
  })
  const chosen = new Set(value.map((a) => a.address.toLowerCase()))
  const options = draft.trim().length >= 2 ? (suggestions.data ?? []).filter((r) => !chosen.has(r.address.toLowerCase())) : []
  const expanded = !!suggest && open && options.length > 0
  const activeIndex = Math.min(active, options.length - 1)
  const listboxId = `${id}-suggestions`

  function pick(r: Recipient) {
    onChange([...value, r.name ? { name: r.name, address: r.address } : { address: r.address }])
    setDraft("")
    setError("")
    setOpen(false)
  }

  function commit(text: string) {
    const parts = text.split(/[,;\n]/).map((p) => p.trim()).filter(Boolean)
    if (parts.length === 0) return true
    const parsed = parts.map(parse)
    const bad = parts.filter((_, i) => !parsed[i])
    const good = parsed.filter((a): a is Address => a !== null)
    const known = new Set(value.map((a) => a.address.toLowerCase()))
    onChange([...value, ...good.filter((a) => !known.has(a.address.toLowerCase()))])
    setDraft(bad.join(", "))
    setError(bad.length ? `${bad.join(", ")} is not a valid email address.` : "")
    return bad.length === 0
  }

  return (
    <div className="relative flex flex-col gap-2">
      <div
        aria-invalid={invalid || !!error || undefined}
        className="flex min-h-10 w-full flex-wrap items-center gap-2 rounded-lg border border-input bg-sunken px-3 py-1 focus-ring-within transition-colors duration-160 aria-invalid:border-danger"
      >
        {value.map((a) => (
          <span key={a.address} className="inline-flex max-w-full items-center gap-1 rounded-xs bg-muted py-1 pr-1 pl-2 text-sm">
            <span className="truncate">{formatAddress(a)}</span>
            <button
              type="button"
              aria-label={`Remove ${a.address}`}
              onClick={() => onChange(value.filter((v) => v !== a))}
              className="inline-flex size-5 cursor-pointer items-center justify-center rounded-xs text-fg-tertiary hover:text-foreground"
            >
              <X className="size-3.5" />
            </button>
          </span>
        ))}
        <input
          id={id}
          value={draft}
          placeholder={value.length ? undefined : placeholder}
          autoComplete="off"
          spellCheck={false}
          data-focus-ring="off"
          aria-describedby={error ? `${id}-error` : undefined}
          role={suggest ? "combobox" : undefined}
          aria-autocomplete={suggest ? "list" : undefined}
          aria-expanded={suggest ? expanded : undefined}
          aria-controls={suggest ? listboxId : undefined}
          aria-activedescendant={expanded ? `${listboxId}-${activeIndex}` : undefined}
          onChange={(e) => {
            const text = e.target.value
            setOpen(true)
            setActive(0)
            if (/[,;]/.test(text)) commit(text)
            else setDraft(text)
          }}
          onKeyDown={(e) => {
            if (expanded && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
              e.preventDefault()
              setActive((activeIndex + (e.key === "ArrowDown" ? 1 : options.length - 1)) % options.length)
            } else if (expanded && e.key === "Enter") {
              e.preventDefault()
              pick(options[activeIndex])
            } else if (expanded && e.key === "Escape") {
              e.preventDefault()
              e.stopPropagation()
              setOpen(false)
            } else if (e.key === "Enter" && draft.trim()) {
              e.preventDefault()
              commit(draft)
            } else if (e.key === "Backspace" && !draft && value.length) {
              onChange(value.slice(0, -1))
            }
          }}
          onBlur={() => {
            setOpen(false)
            commit(draft)
          }}
          onPaste={(e) => {
            const text = e.clipboardData.getData("text")
            if (/[,;\n]/.test(text)) {
              e.preventDefault()
              commit(draft + text)
            }
          }}
          className="h-8 min-w-[160px] flex-1 bg-transparent text-base outline-none placeholder:text-fg-tertiary"
        />
      </div>
      {suggest && (
        <ul
          id={listboxId}
          role="listbox"
          aria-label="Suggested recipients"
          hidden={!expanded}
          className="absolute inset-x-0 top-full z-50 mt-1 flex max-h-72 flex-col overflow-y-auto rounded-lg border border-strong bg-popover p-1 shadow-popover"
        >
          {options.map((r, i) => (
            <li
              key={r.address}
              id={`${listboxId}-${i}`}
              role="option"
              aria-selected={i === activeIndex}
              onMouseDown={(e) => e.preventDefault()}
              onMouseEnter={() => setActive(i)}
              onClick={() => pick(r)}
              className="flex min-h-11 cursor-pointer items-center gap-3 rounded-md px-3 py-2 aria-selected:bg-muted"
            >
              <span className="flex min-w-0 flex-1 flex-col">
                {r.name && <span className="truncate">{r.name}</span>}
                <span className="truncate font-mono text-sm text-fg-tertiary">{r.address}</span>
              </span>
              <span className="shrink-0 rounded-full bg-muted px-2 py-1 text-xs font-semibold text-fg-secondary">{r.source === "contact" ? "Contact" : "Directory"}</span>
            </li>
          ))}
        </ul>
      )}
      {error && (
        <p id={`${id}-error`} role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
    </div>
  )
}

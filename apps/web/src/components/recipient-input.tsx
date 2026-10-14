"use client"

import { X } from "lucide-react"
import { useState } from "react"
import { z } from "zod"

import { formatAddress } from "@/lib/format"
import type { Address } from "@/lib/types"

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
}: {
  id: string
  value: Address[]
  onChange: (value: Address[]) => void
  invalid?: boolean
  placeholder?: string
}) {
  const [draft, setDraft] = useState("")
  const [error, setError] = useState("")

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
    <div className="flex flex-col gap-2">
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
          onChange={(e) => {
            const text = e.target.value
            if (/[,;]/.test(text)) commit(text)
            else setDraft(text)
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && draft.trim()) {
              e.preventDefault()
              commit(draft)
            } else if (e.key === "Backspace" && !draft && value.length) {
              onChange(value.slice(0, -1))
            }
          }}
          onBlur={() => commit(draft)}
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
      {error && (
        <p id={`${id}-error`} role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
    </div>
  )
}

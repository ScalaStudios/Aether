"use client"

import { Check, Copy } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { IconButton } from "@/components/icon-button"
import { Input } from "@/components/ui/input"

export function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    } catch {
      toast.error("Copying failed. Select the text and copy it manually.")
    }
  }

  return (
    <IconButton label={copied ? "Copied" : label} onClick={copy} size="icon-sm">
      {copied ? <Check className="text-success" /> : <Copy />}
    </IconButton>
  )
}

export function CopyField({ id, label, value, copyLabel }: { id: string; label: string; value: string; copyLabel: string }) {
  return (
    <Field id={id} label={label}>
      <div className="flex items-center gap-2">
        <Input id={id} readOnly value={value} className="font-mono" onFocus={(e) => e.currentTarget.select()} />
        <CopyButton value={value} label={copyLabel} />
      </div>
    </Field>
  )
}

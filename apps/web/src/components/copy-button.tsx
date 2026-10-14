"use client"

import { Check, Copy } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { IconButton } from "@/components/icon-button"

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

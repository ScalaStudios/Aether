"use client"

import { useEffect, useRef, useState } from "react"

import { Button } from "@/components/ui/button"
import type { ScheduledMessage } from "@/lib/types"

export type UndoHold = ScheduledMessage & { mailboxId: string }

export function isUndoHold(message: ScheduledMessage) {
  return message.undo && !message.error
}

function recipients(message: ScheduledMessage) {
  const names = message.to.map((address) => address.name || address.address).filter(Boolean)
  if (!names.length) return "the recipients"
  if (names.length <= 3) return names.join(", ")
  return `${names.slice(0, 2).join(", ")} and ${names.length - 2} more`
}

export function useNow(running: boolean) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!running) return
    const tick = () => setNow(Date.now())
    const start = window.setTimeout(tick, 0)
    const id = window.setInterval(tick, 250)
    return () => {
      window.clearTimeout(start)
      window.clearInterval(id)
    }
  }, [running])
  return now
}

export function UndoSendBar({
  items,
  undoing,
  onUndo,
}: {
  items: UndoHold[]
  undoing?: string
  onUndo: (item: UndoHold) => void
}) {
  const now = useNow(items.length > 0)
  const known = useRef(new Set<string>())
  const [notice, setNotice] = useState("")
  const itemKey = items.map((item) => item.id).join(" ")

  useEffect(() => {
    const fresh = items.filter((item) => !known.current.has(item.id))
    for (const item of items) known.current.add(item.id)
    const newest = fresh[fresh.length - 1]
    if (!newest) return
    const active = document.activeElement
    const typing = active instanceof HTMLElement && (active.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(active.tagName))
    if (!typing && !document.querySelector('[role="dialog"]')) {
      document.getElementById(`undo-send-${newest.id}`)?.focus()
      return
    }
    setNotice(`Sending to ${recipients(newest)}. You can undo it for a few seconds.`)
  }, [itemKey, items])

  if (!items.length) return null

  return (
    <section aria-label="Undo send" className="border-t border-border bg-card px-4 py-3">
      <p className="sr-only" role="status">{notice}</p>
      <ul className="mx-auto flex max-h-[40dvh] w-full max-w-3xl flex-col gap-3 overflow-y-auto">
        {items.map((item) => {
          const seconds = Math.max(0, Math.ceil((Date.parse(item.sendAt) - now) / 1000))
          const who = recipients(item)
          return (
            <li key={item.id} className="flex flex-col gap-3 border-l-2 border-ember pl-3 sm:flex-row sm:items-center">
              <div className="min-w-0 flex-1">
                <p className="truncate font-semibold">Sending to {who}</p>
                <p className="truncate text-sm text-fg-tertiary">{item.subject || "(no subject)"}</p>
                <p className="text-sm text-fg-tertiary tabular-nums" aria-hidden="true">
                  {seconds > 0 ? `${seconds} ${seconds === 1 ? "second" : "seconds"} left` : "Sending now"}
                </p>
              </div>
              <Button
                id={`undo-send-${item.id}`}
                type="button"
                variant="secondary"
                className="w-full max-md:min-h-11 sm:w-auto"
                loading={undoing === item.id}
                aria-label={`Undo sending to ${who}`}
                onClick={() => onUndo(item)}
              >
                Undo
              </Button>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

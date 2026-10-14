"use client"

import { useState } from "react"

import { Field } from "@/components/field"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  typedConfirmation,
  pending,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: React.ReactNode
  confirmLabel: string
  typedConfirmation?: string
  pending?: boolean
  onConfirm: () => void
}) {
  const [typed, setTyped] = useState("")
  const blocked = typedConfirmation !== undefined && typed !== typedConfirmation

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setTyped("")
        onOpenChange(next)
      }}
    >
      <DialogContent role="alertdialog">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {typedConfirmation !== undefined && (
          <Field id="confirm-typed" label={`Type ${typedConfirmation} to confirm`}>
            <Input id="confirm-typed" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} className="font-mono" />
          </Field>
        )}
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="secondary">Cancel</Button>
          </DialogClose>
          <Button variant="destructive" disabled={blocked} loading={pending} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

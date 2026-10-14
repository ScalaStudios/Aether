"use client"

import { Controller, type Control, type FieldPathByValue, type FieldValues } from "react-hook-form"

import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

export function SwitchField<T extends FieldValues>({
  control,
  name,
  id,
  label,
  hint,
}: {
  control: Control<T>
  name: FieldPathByValue<T, boolean>
  id: string
  label: string
  hint?: string
}) {
  return (
    <Controller
      control={control}
      name={name}
      render={({ field }) => (
        <div className="flex items-start gap-3">
          <Switch id={id} checked={field.value} onCheckedChange={field.onChange} className="mt-1" />
          <div className="flex flex-col gap-1">
            <Label htmlFor={id}>{label}</Label>
            {hint && <p className="text-sm text-fg-tertiary">{hint}</p>}
          </div>
        </div>
      )}
    />
  )
}

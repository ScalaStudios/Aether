"use client"

import { Controller, type Control, type FieldPathByValue, type FieldValues } from "react-hook-form"

import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

export function SwitchRow({
  id,
  label,
  hint,
  children,
  ...props
}: Omit<React.ComponentProps<typeof Switch>, "children"> & {
  id: string
  label: string
  hint?: string
  children?: React.ReactNode
}) {
  return (
    <div className="flex items-start gap-3">
      <Switch id={id} className="mt-1" {...props} />
      <div className="flex flex-col gap-1">
        <Label htmlFor={id}>{label}</Label>
        {hint && <p className="text-sm text-fg-tertiary">{hint}</p>}
        {children}
      </div>
    </div>
  )
}

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
      render={({ field }) => <SwitchRow id={id} label={label} hint={hint} checked={field.value} onCheckedChange={field.onChange} />}
    />
  )
}

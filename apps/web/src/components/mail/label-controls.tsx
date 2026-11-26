"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Plus, Tag } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"
import { cn } from "@/lib/utils"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { api, errorMessage } from "@/lib/api"
import { appliedLabels, labelColorOrder, labelPalette, showFlagColor } from "@/lib/mail"
import type { Label, LabelColor } from "@/lib/types"

const schema = z.object({
  name: z.string().trim().min(1, "Name the label.").max(40, "Use a name of up to 40 characters."),
  color: z.enum(labelColorOrder),
})

type Values = z.infer<typeof schema>

export function LabelDot({ color, label }: { color: string; label?: string }) {
  const known = color in labelPalette ? labelPalette[color as LabelColor] : labelPalette.gray
  return (
    <span
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      className="size-2 shrink-0 rounded-full"
      style={{ backgroundColor: known.hex }}
    />
  )
}

export function LabelChips({ labels, keywords, flagColor }: { labels: Pick<Label, "id" | "keyword" | "name" | "color">[]; keywords?: string[]; flagColor?: string }) {
  const applied = appliedLabels(keywords, labels)
  const flag = showFlagColor(flagColor, applied) ? labelPalette[flagColor as LabelColor] : undefined
  if (!applied.length && !flag) return null
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-1">
      {flag && flagColor && <LabelDot color={flagColor} label={`${flag.name} flag`} />}
      {applied.map((label) => (
        <span key={label.keyword} className="inline-flex max-w-36 items-center gap-1 rounded-full border border-border px-1.5 py-0.5 text-[11px] text-fg-secondary">
          <LabelDot color={label.color} />
          <span className="truncate">{label.name}</span>
        </span>
      ))}
    </span>
  )
}

export function LabelDialog({
  mailboxId,
  label,
  open,
  onOpenChange,
}: {
  mailboxId: string
  label?: Label
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const qc = useQueryClient()
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { name: label?.name ?? "", color: label?.color ?? "red" } })
  const [error, setError] = useState("")
  const save = useMutation({
    meta: { silent: true },
    mutationFn: (values: Values) =>
      api<Label>(`/mailboxes/${mailboxId}/labels${label ? `/${label.id}` : ""}`, { method: label ? "PATCH" : "POST", body: values }),
    onSuccess: (saved) => {
      qc.invalidateQueries({ queryKey: ["labels", mailboxId] })
      toast.success(label ? `Saved ${saved.name}` : `Created ${saved.name}`)
      onOpenChange(false)
    },
    onError: (e) => setError(errorMessage(e)),
  })

  return (
    <FormDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setError("")
        onOpenChange(next)
      }}
      title={label ? "Edit label" : "New label"}
      description="Labels can sit on a message together with its folder. Other mail apps see them as tags."
      submitLabel={label ? "Save label" : "Create label"}
      pending={save.isPending}
      onSubmit={form.handleSubmit((values) => {
        setError("")
        save.mutate(values)
      })}
    >
      <Field id="label-name" label="Name" error={form.formState.errors.name?.message || error}>
        <Input id="label-name" autoComplete="off" autoFocus aria-invalid={!!form.formState.errors.name || !!error} {...form.register("name")} />
      </Field>
      <Controller
        control={form.control}
        name="color"
        render={({ field }) => (
          <fieldset className="flex flex-col gap-2">
            <legend className="text-sm font-medium text-foreground">Color</legend>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {labelColorOrder.map((color) => (
                <label
                  key={color}
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded-lg border border-border px-2 py-2 text-sm text-fg-secondary",
                    field.value === color && "text-foreground ring-2 ring-ember ring-offset-2 ring-offset-card"
                  )}
                >
                  <input type="radio" className="sr-only" name="label-color" value={color} checked={field.value === color} onChange={() => field.onChange(color)} />
                  <LabelDot color={color} />
                  {labelPalette[color].name}
                </label>
              ))}
            </div>
          </fieldset>
        )}
      />
    </FormDialog>
  )
}

export function LabelMenu({
  labels,
  keywords,
  disabled,
  onToggle,
  onCreate,
}: {
  labels: Label[]
  keywords?: string[]
  disabled?: boolean
  onToggle: (label: Label, on: boolean) => void
  onCreate: () => void
}) {
  const applied = new Set(keywords ?? [])
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button variant="quiet" size="icon" aria-label="Labels" aria-pressed={applied.size > 0} disabled={disabled}>
              <Tag className={cn(applied.size > 0 && "text-ember")} />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>Labels</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">Labels</DropdownMenuLabel>
        {labels.length === 0 ? (
          <DropdownMenuItem disabled>No labels yet</DropdownMenuItem>
        ) : (
          labels.map((label) => (
            <DropdownMenuCheckboxItem
              key={label.id}
              checked={applied.has(label.keyword)}
              onCheckedChange={(checked) => onToggle(label, checked === true)}
              onSelect={(event) => event.preventDefault()}
            >
              <LabelDot color={label.color} />
              <span className="truncate">{label.name}</span>
            </DropdownMenuCheckboxItem>
          ))
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={onCreate}>
          <Plus />
          New label…
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function SearchDialog({
  open,
  onOpenChange,
  title,
  description,
  submitLabel,
  initialName,
  pending,
  error,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  submitLabel: string
  initialName?: string
  pending?: boolean
  error?: string
  onSubmit: (name: string) => void
}) {
  const [name, setName] = useState(initialName ?? "")
  const [invalid, setInvalid] = useState(false)
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={description}
      submitLabel={submitLabel}
      pending={pending}
      onSubmit={(event) => {
        event.preventDefault()
        const next = name.trim()
        if (!next || next.length > 80) {
          setInvalid(true)
          return
        }
        onSubmit(next)
      }}
    >
      <Field id="search-name" label="Name" error={invalid ? "Use a name of up to 80 characters." : error}>
        <Input
          id="search-name"
          autoComplete="off"
          autoFocus
          maxLength={80}
          value={name}
          aria-invalid={invalid || !!error}
          onChange={(event) => {
            setName(event.target.value)
            setInvalid(false)
          }}
        />
      </Field>
    </FormDialog>
  )
}

"use client"

import { useQuery } from "@tanstack/react-query"
import { Controller, type Control, type FieldValues, type Path } from "react-hook-form"

import { Field } from "@/components/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { domainsQuery } from "@/lib/queries"

export function DomainSelect<T extends FieldValues & { domainId: string }>({
  id,
  orgId,
  control,
}: {
  id: string
  orgId: string
  control: Control<T>
}) {
  const domains = useQuery(domainsQuery(orgId))

  return (
    <Controller
      control={control}
      name={"domainId" as Path<T>}
      render={({ field, fieldState }) => (
        <Field id={id} label="Domain" error={fieldState.error?.message}>
          <Select value={field.value} onValueChange={field.onChange}>
            <SelectTrigger id={id} aria-invalid={!!fieldState.error} className="w-full">
              <SelectValue placeholder={domains.isPending ? "Loading domains…" : domains.data?.length ? "Choose a domain" : "Add a domain first"} />
            </SelectTrigger>
            <SelectContent>
              {domains.data?.map((d) => (
                <SelectItem key={d.id} value={d.id}>
                  @{d.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
    />
  )
}

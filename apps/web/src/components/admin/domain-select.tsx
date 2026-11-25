"use client"

import { useQuery } from "@tanstack/react-query"
import { Controller, type Control, type FieldValues, type Path } from "react-hook-form"

import { Field } from "@/components/field"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useI18n } from "@/lib/i18n"
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
  const { t } = useI18n()
  const domains = useQuery({ ...domainsQuery(orgId), select: (items) => items.filter((d) => !d.aliasOf) })

  return (
    <Controller
      control={control}
      name={"domainId" as Path<T>}
      render={({ field, fieldState }) => (
        <Field id={id} label={t("admin.domainSelect.label")} error={fieldState.error?.message}>
          <Select value={field.value} onValueChange={field.onChange}>
            <SelectTrigger id={id} aria-invalid={!!fieldState.error} className="w-full">
              <SelectValue
                placeholder={
                  domains.isPending
                    ? t("admin.domainSelect.loading")
                    : domains.data?.length
                      ? t("admin.domainSelect.choose")
                      : t("admin.domainSelect.addFirst")
                }
              />
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

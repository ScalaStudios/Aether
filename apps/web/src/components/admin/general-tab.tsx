"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { LOCALE_LABELS, SUPPORTED_LOCALES, useI18n } from "@/lib/i18n"
import { organizationsQuery } from "@/lib/queries"
import type { Organization } from "@/lib/types"

const card = "flex flex-col gap-4 rounded-xl border border-border bg-card p-6"
const AUTO = "auto"

export function GeneralTab({ orgId }: { orgId: string }) {
  const orgQuery = useQuery({
    ...organizationsQuery,
    select: (orgs) => orgs.find((o) => o.id === orgId) ?? null,
  })

  return (
    <QueryView query={orgQuery} skeleton={<RowsSkeleton rows={3} />} isEmpty={(o) => !o}>
      {(org) => org && <OrganizationGeneral key={`${org.id}-${org.defaultLocale}`} org={org} />}
    </QueryView>
  )
}

function OrganizationGeneral({ org }: { org: Organization }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const initial = org.defaultLocale || AUTO
  const [selected, setSelected] = useState(initial)

  const save = useMutation({
    mutationFn: (defaultLocale: string) =>
      api<Organization>(`/organizations/${org.id}`, {
        method: "PATCH",
        body: { defaultLocale },
      }),
    onSuccess: (updated) => {
      toast.success(t("admin.general.savedToast"))
      qc.setQueryData<Organization[]>(["organizations"], (prev) => prev?.map((o) => (o.id === updated.id ? updated : o)))
      qc.invalidateQueries({ queryKey: ["organizations"] })
      qc.invalidateQueries({ queryKey: ["session"] })
      qc.invalidateQueries({ queryKey: ["auth-providers"] })
    },
  })

  const dirty = selected !== initial

  return (
    <div className="flex flex-col gap-6">
      <section className={card}>
        <SectionHeader title={t("admin.general.title")} description={t("admin.general.description")} />
        <dl className="grid gap-4 sm:grid-cols-3">
          <div className="flex flex-col gap-1">
            <dt className="text-label text-fg-tertiary">{t("common.name")}</dt>
            <dd className="text-sm font-medium">{org.name}</dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="text-label text-fg-tertiary">{t("admin.slug")}</dt>
            <dd className="font-mono text-sm">{org.slug}</dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="text-label text-fg-tertiary">{t("admin.general.created")}</dt>
            <dd className="text-sm">{formatDate(org.createdAt)}</dd>
          </div>
        </dl>
      </section>

      <form
        noValidate
        className={card}
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate(selected === AUTO ? "" : selected)
        }}
      >
        <SectionHeader title={t("admin.general.languageSectionTitle")} description={t("admin.general.languageSectionDescription")} />
        <Field id="org-default-locale" label={t("admin.defaultLanguage")} hint={t("admin.defaultLanguageHint")}>
          <div className="flex flex-col gap-3 sm:flex-row">
            <Select value={selected} onValueChange={setSelected}>
              <SelectTrigger id="org-default-locale" className="w-full sm:max-w-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={AUTO}>{t("admin.browserDefault")}</SelectItem>
                {SUPPORTED_LOCALES.map((code) => (
                  <SelectItem key={code} value={code}>
                    {LOCALE_LABELS[code]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button type="submit" variant="secondary" loading={save.isPending} disabled={!dirty}>
              {t("admin.general.saveLanguage")}
            </Button>
          </div>
        </Field>
      </form>
    </div>
  )
}

"use client"

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Globe, Info, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { Field } from "@/components/field"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { MailboxPicker } from "@/components/settings/mail-rules"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { useSession } from "@/hooks/use-session"
import { api, errorMessage } from "@/lib/api"
import { useI18n } from "@/lib/i18n"
import { messageDocument } from "@/lib/mail"
import { hasPerm, isDomainScoped } from "@/lib/permissions"
import { domainsQuery, mailboxesQuery } from "@/lib/queries"
import type { Domain, SignatureTemplate } from "@/lib/types"

const placeholders = ["displayName", "title", "phone", "organization", "domain", "address", "logo", "links"]

export function SignaturesTab({ orgId }: { orgId: string }) {
  const session = useSession().data
  const canWrite = hasPerm(session, orgId, "signatures:write")
  const scoped = isDomainScoped(session, orgId)
  const { t } = useI18n()
  const templates = useQuery({
    queryKey: ["signature-templates", orgId],
    queryFn: () => api<{ builtIn: SignatureTemplate; templates: SignatureTemplate[] }>(`/organizations/${orgId}/signature-templates`),
  })
  const domains = useQuery(domainsQuery(orgId))
  const [scope, setScope] = useState("all")
  const domainList = domains.data ?? []
  const effectiveScope = scoped ? (domainList.some((d) => d.id === scope) ? scope : domainList[0]?.id ?? "") : scope
  const domain = domainList.find((d) => d.id === effectiveScope)

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title={t("admin.signatures.title")}
        description={t("admin.signatures.description")}
        actions={
          domainList.length > 0 && (
            <Select value={effectiveScope} onValueChange={setScope}>
              <SelectTrigger aria-label={t("admin.signatures.templateFor")} className="w-64">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {!scoped && <SelectItem value="all">All domains</SelectItem>}
                {domainList.map((d) => (
                  <SelectItem key={d.id} value={d.id}>
                    @{d.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )
        }
      />
      {scoped && domains.isPending ? (
        <RowsSkeleton rows={4} />
      ) : scoped && domainList.length === 0 ? (
        <EmptyState icon={Globe} title="No assigned domains" description="Ask an administrator to assign at least one domain to your role." />
      ) : (
        <QueryView query={templates} skeleton={<RowsSkeleton rows={4} />}>
          {(data) => {
            const own = data.templates.find((item) => item.domainId === (domain?.id ?? null))
            const fallback = data.templates.find((item) => item.domainId === null) ?? data.builtIn
            return (
              <TemplateEditor
                key={`${effectiveScope}-${!!own}`}
                orgId={orgId}
                domain={domain}
                template={own ?? { ...fallback, domainId: domain?.id ?? null }}
                builtIn={data.builtIn}
                stored={!!own}
                canWrite={canWrite}
              />
            )
          }}
        </QueryView>
      )}
    </section>
  )
}

function TemplateEditor({
  orgId,
  domain,
  template,
  builtIn,
  stored,
  canWrite,
}: {
  orgId: string
  domain?: Domain
  template: SignatureTemplate
  builtIn: SignatureTemplate
  stored: boolean
  canWrite: boolean
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const session = useSession().data
  const [html, setHtml] = useState(template.html)
  const [links, setLinks] = useState(template.links)
  const [logoWidth, setLogoWidth] = useState(String(template.logoWidth))
  const [chosen, setChosen] = useState<string | null>(null)
  const mailboxes = useQuery({
    ...mailboxesQuery(orgId),
    select: (list) => list.filter((m) => !domain || m.domainId === domain.id),
  })
  const mailboxId = chosen ?? (mailboxes.data?.find((m) => m.address === session?.user.email) ?? mailboxes.data?.[0])?.id ?? null
  const draft = useDebouncedValue(JSON.stringify({ html, links, logoWidth: Number(logoWidth) }), 400)
  const preview = useQuery({
    queryKey: ["signature-preview", orgId, mailboxId, draft],
    queryFn: () => api<{ html: string }>(`/organizations/${orgId}/signature-templates/preview`, { method: "POST", body: { ...JSON.parse(draft), mailboxId } }),
    enabled: !!mailboxId,
    placeholderData: keepPreviousData,
  })
  const done = (message: string) => {
    toast.success(message)
    qc.invalidateQueries({ queryKey: ["signature-templates", orgId] })
  }
  const save = useMutation({
    mutationFn: () => api<SignatureTemplate>(`/organizations/${orgId}/signature-templates`, { method: "PUT", body: { domainId: template.domainId, html, links, logoWidth: Number(logoWidth) } }),
    onSuccess: () => done(domain ? t("admin.signatures.savedDomainToast", { domain: domain.name }) : t("admin.signatures.savedToast")),
  })
  const remove = useMutation({
    mutationFn: () => api(`/organizations/${orgId}/signature-templates`, { method: "DELETE", query: { domainId: template.domainId } }),
    onSuccess: () => done(t("admin.signatures.revertedToast", { domain: domain?.name ?? "" })),
  })
  const setLink = (i: number, change: Partial<SignatureTemplate["links"][number]>) => setLinks((list) => list.map((l, j) => (j === i ? { ...l, ...change } : l)))

  return (
    <form
      noValidate
      className="animate-content flex flex-col gap-6 rounded-xl border border-border bg-card p-6"
      onSubmit={(e) => {
        e.preventDefault()
        if (!canWrite) return
        save.mutate()
      }}
    >
      {domain && !stored && (
        <p className="flex gap-3 text-sm text-fg-secondary">
          <Info aria-hidden className="size-4 shrink-0 translate-y-0.5 text-info" />
          {canWrite
            ? `@${domain.name} uses the template for all domains. Saving here gives it a template of its own.`
            : `@${domain.name} uses the template for all domains.`}
        </p>
      )}
      <div className="grid gap-6 lg:grid-cols-2">
        <div className="flex flex-col gap-4">
          <Field
            id="template-html"
            label={t("admin.signatures.template")}
            hint={
              <>
                {t("admin.signatures.placeholdersPrefix")}
                {placeholders.map((p, i) => (
                  <span key={p}>
                    <code className="font-mono text-xs">{`{{${p}}}`}</code>
                    {i < placeholders.length - 1 ? ", " : ". "}
                  </span>
                ))}
                {t("admin.signatures.conditionalHintBefore")}
                <code className="font-mono text-xs">{"{{#title}}…{{/title}}"}</code>
                {t("admin.signatures.conditionalHintAfter")}
              </>
            }
          >
            <Textarea id="template-html" rows={14} spellCheck={false} readOnly={!canWrite} value={html} className="font-mono text-sm" onChange={(e) => setHtml(e.target.value)} />
          </Field>
          <Field id="template-logo" label={t("admin.signatures.logoWidth")} hint={t("admin.signatures.logoWidthHint")}>
            <Input id="template-logo" type="number" min={16} max={240} readOnly={!canWrite} value={logoWidth} className="w-28" onChange={(e) => setLogoWidth(e.target.value)} />
          </Field>
          <fieldset className="flex flex-col gap-2">
            <legend className="pb-2 text-label text-fg-secondary">{t("admin.signatures.links")}</legend>
            {links.length === 0 && !canWrite && <p className="text-sm text-fg-tertiary">No links configured.</p>}
            {links.map((l, i) => (
              <div key={i} className="flex gap-2">
                <Input aria-label={`Label of link ${i + 1}`} placeholder="LinkedIn" maxLength={40} readOnly={!canWrite} value={l.label} className="w-36 shrink-0" onChange={(e) => setLink(i, { label: e.target.value })} />
                <Input aria-label={`Address of link ${i + 1}`} placeholder="https://" readOnly={!canWrite} value={l.url} spellCheck={false} onChange={(e) => setLink(i, { url: e.target.value })} />
                {canWrite && (
                  <IconButton label={t("admin.signatures.removeLinkAria", { index: i + 1 })} onClick={() => setLinks((list) => list.filter((_, j) => j !== i))}>
                    <Trash2 />
                  </IconButton>
                )}
              </div>
            ))}
            {canWrite && (
              <Button type="button" variant="secondary" size="sm" className="self-start" disabled={links.length >= 10} onClick={() => setLinks((list) => [...list, { label: "", url: "" }])}>
                <Plus aria-hidden />
                {t("admin.signatures.addLink")}
              </Button>
            )}
            <p className="text-sm text-fg-tertiary">{t("admin.signatures.linksHint")}</p>
          </fieldset>
        </div>
        <div className="flex flex-col gap-2">
          <div className="flex min-h-10 flex-wrap items-center justify-between gap-3">
            <span className="text-label text-fg-secondary">{t("admin.signatures.preview")}</span>
            <MailboxPicker mailboxes={mailboxes.data} value={mailboxId} onChange={setChosen} />
          </div>
          {mailboxes.isPending || (mailboxId && preview.isPending) ? (
            <RowsSkeleton rows={2} />
          ) : !mailboxId ? (
            <p className="rounded-lg border border-border p-6 text-sm text-fg-secondary">
              {domain ? t("admin.signatures.addMailboxDomainPreview", { domain: domain.name }) : t("admin.signatures.addMailboxPreview")}
            </p>
          ) : (
            <iframe
              title={t("admin.signatures.previewTitle")}
              sandbox="allow-popups allow-popups-to-escape-sandbox"
              srcDoc={messageDocument(preview.data?.html ?? "")}
              className={cn("h-64 w-full rounded-lg border border-border bg-white transition-opacity duration-160", preview.isFetching && "opacity-60")}
            />
          )}
          {preview.isError && (
            <p role="alert" className="text-sm text-danger">
              {errorMessage(preview.error)}
            </p>
          )}
        </div>
      </div>
      {canWrite && (
        <div className="flex flex-wrap gap-3">
          <Button type="submit" variant="secondary" loading={save.isPending}>
            {t("admin.signatures.saveTemplate")}
          </Button>
          {domain && stored ? (
            <Button type="button" variant="quiet" loading={remove.isPending} onClick={() => remove.mutate()}>
              Use the template for all domains
            </Button>
          ) : (
            <Button
              type="button"
              variant="quiet"
              disabled={html === builtIn.html && logoWidth === String(builtIn.logoWidth)}
              onClick={() => {
                setHtml(builtIn.html)
                setLogoWidth(String(builtIn.logoWidth))
              }}
            >
              {t("admin.signatures.restoreDefault")}
            </Button>
          )}
        </div>
      )}
    </form>
  )
}

"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { CalendarDays, CircleAlert, ImageUp, Mail } from "lucide-react"
import { useTheme } from "next-themes"
import { useRef, useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { aetherColors, brandColorFields, brandThemes, brandVariables } from "@/lib/branding"
import { formatBytes } from "@/lib/format"
import { hasPerm } from "@/lib/permissions"
import { useI18n } from "@/lib/i18n"
import { organizationsQuery } from "@/lib/queries"
import type { BrandColors, BrandingSettings, BrandTheme, OrgBranding } from "@/lib/types"

const card = "flex flex-col gap-4 rounded-xl border border-border bg-card p-6"

const hexColor = /^#[0-9a-f]{6}$/

function useSaveBranding(orgId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (settings: BrandingSettings) => api<OrgBranding>(`/organizations/${orgId}/branding`, { method: "PUT", body: settings }),
    onSuccess: (saved) => {
      qc.setQueryData(["org-branding", orgId], saved)
      qc.invalidateQueries({ queryKey: ["branding"] })
    },
  })
}

function settingsOf({ name, signInMessage, colors, customCss }: OrgBranding): BrandingSettings {
  return { name, signInMessage, colors, customCss }
}

export function BrandingTab({ orgId }: { orgId: string }) {
  const session = useSession().data
  const canWrite = hasPerm(session, orgId, "branding:write")
  const branding = useQuery({ queryKey: ["org-branding", orgId], queryFn: () => api<OrgBranding>(`/organizations/${orgId}/branding`) })

  return (
    <QueryView query={branding} skeleton={<RowsSkeleton rows={4} />}>
      {(data) => (
        <div className="flex flex-col gap-6">
          <NameAndLogo orgId={orgId} branding={data} canWrite={canWrite} />
          <SignInMessage orgId={orgId} branding={data} canWrite={canWrite} />
          <Colours orgId={orgId} branding={data} canWrite={canWrite} />
          <CustomCSS orgId={orgId} branding={data} canWrite={canWrite} />
        </div>
      )}
    </QueryView>
  )
}

function NameAndLogo({ orgId, branding, canWrite }: { orgId: string; branding: OrgBranding; canWrite: boolean }) {
  const { t } = useI18n()
  const orgName = useQuery({ ...organizationsQuery, select: (orgs) => orgs.find((o) => o.id === orgId)?.name ?? "" }).data ?? ""
  const [name, setName] = useState(branding.name)
  const save = useSaveBranding(orgId)

  return (
    <section className={card}>
      <SectionHeader title={t("admin.branding.nameAndLogoTitle")} description={t("admin.branding.nameAndLogoDescription")} />
      <form
        noValidate
        onSubmit={(e) => {
          e.preventDefault()
          if (!canWrite) return
          save.mutate({ ...settingsOf(branding), name: name.trim() }, { onSuccess: () => toast.success(t("admin.branding.nameSavedToast")) })
        }}
      >
        <Field id="brand-name" label={t("common.name")} hint={orgName ? t("admin.branding.nameHint", { orgName }) : undefined}>
          <div className="flex flex-col gap-3 sm:flex-row">
            <Input id="brand-name" value={name} maxLength={100} autoComplete="off" readOnly={!canWrite} placeholder={orgName} className="sm:max-w-sm" onChange={(e) => setName(e.target.value)} />
            {canWrite && (
              <Button type="submit" variant="secondary" loading={save.isPending} disabled={name.trim() === branding.name}>
                {t("admin.branding.saveName")}
              </Button>
            )}
          </div>
        </Field>
      </form>
      <div className="grid gap-6 pt-2 sm:grid-cols-3">
        <ImageSlot orgId={orgId} kind="logo-light" label="Light theme logo" url={branding.logoLight} theme="light" canWrite={canWrite} />
        <ImageSlot orgId={orgId} kind="logo-dark" label="Dark theme logo" url={branding.logoDark} theme="dark" canWrite={canWrite} />
        <ImageSlot orgId={orgId} kind="favicon" label="Favicon" url={branding.favicon} canWrite={canWrite} />
      </div>
      <p className="text-sm text-fg-tertiary">{t("admin.branding.imageRequirements")}</p>
    </section>
  )
}

function ImageSlot({ orgId, kind, label, url, theme, canWrite }: { orgId: string; kind: string; label: string; url: string | null; theme?: BrandTheme; canWrite: boolean }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const input = useRef<HTMLInputElement>(null)
  const done = (saved: OrgBranding, message: string) => {
    qc.setQueryData(["org-branding", orgId], saved)
    qc.invalidateQueries({ queryKey: ["branding"] })
    toast.success(message)
  }
  const upload = useMutation({
    mutationFn: (file: File) => api<OrgBranding>(`/organizations/${orgId}/branding/${kind}`, { method: "PUT", body: file }),
    onSuccess: (saved) => done(saved, t("admin.branding.uploadedToast", { label })),
  })
  const remove = useMutation({
    mutationFn: () => api<OrgBranding>(`/organizations/${orgId}/branding/${kind}`, { method: "DELETE" }),
    onSuccess: (saved) => done(saved, t("admin.branding.removedToast", { label })),
  })

  return (
    <div className="flex flex-col gap-3">
      <span className="text-label text-fg-secondary">{label}</span>
      <div className={cn(theme, "grid h-28 place-items-center rounded-lg border border-border bg-background p-4")}>
        {url ? (
          <img src={url} alt={t("admin.branding.currentImageAlt", { label: label.toLowerCase() })} className={kind === "favicon" ? "size-8" : "max-h-12 max-w-full object-contain"} />
        ) : (
          <span className="text-sm text-fg-tertiary">{t("admin.branding.noneYet")}</span>
        )}
      </div>
      {canWrite && (
        <>
          <input
            ref={input}
            type="file"
            accept="image/png,image/jpeg,image/webp"
            hidden
            onChange={(e) => {
              const file = e.target.files?.[0]
              if (file) upload.mutate(file)
              e.target.value = ""
            }}
          />
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="secondary"
              size="sm"
              aria-label={url ? t("admin.branding.replaceAria", { label: label.toLowerCase() }) : t("admin.branding.uploadAria", { label: label.toLowerCase() })}
              loading={upload.isPending}
              onClick={() => input.current?.click()}
            >
              <ImageUp aria-hidden />
              {url ? t("admin.branding.replace") : t("admin.branding.upload")}
            </Button>
            {url && (
              <Button type="button" variant="quiet" size="sm" aria-label={t("admin.branding.removeAria", { label: label.toLowerCase() })} loading={remove.isPending} onClick={() => remove.mutate()}>
                {t("admin.branding.remove")}
              </Button>
            )}
          </div>
        </>
      )}
    </div>
  )
}

function SignInMessage({ orgId, branding, canWrite }: { orgId: string; branding: OrgBranding; canWrite: boolean }) {
  const { t } = useI18n()
  const [message, setMessage] = useState(branding.signInMessage)
  const save = useSaveBranding(orgId)

  return (
    <form
      noValidate
      className={card}
      onSubmit={(e) => {
        e.preventDefault()
        if (!canWrite) return
        save.mutate(
          { ...settingsOf(branding), signInMessage: message.trim() },
          { onSuccess: () => toast.success(message.trim() ? t("admin.branding.messageSavedToast") : t("admin.branding.messageRemovedToast")) }
        )
      }}
    >
      <SectionHeader title={t("admin.branding.signInMessageTitle")} description={t("admin.branding.signInMessageDescription")} />
      <Field id="brand-message" label={t("admin.branding.message")} hint={t("admin.branding.messageHint", { count: message.length })}>
        <Textarea
          id="brand-message"
          rows={4}
          maxLength={500}
          readOnly={!canWrite}
          value={message}
          placeholder={t("admin.branding.messagePlaceholder")}
          onChange={(e) => setMessage(e.target.value)}
        />
      </Field>
      {canWrite && (
        <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={message.trim() === branding.signInMessage}>
          {t("admin.branding.saveMessage")}
        </Button>
      )}
    </form>
  )
}

function Colours({ orgId, branding, canWrite }: { orgId: string; branding: OrgBranding; canWrite: boolean }) {
  const { t } = useI18n()
  const { resolvedTheme } = useTheme()
  const [theme, setTheme] = useState<BrandTheme>(resolvedTheme === "light" ? "light" : "dark")
  const [colors, setColors] = useState<BrandColors>(branding.colors)
  const [error, setError] = useState("")
  const save = useSaveBranding(orgId)
  const values = colors[theme]
  const customized = brandThemes.some((item) => Object.values(colors[item]).some(Boolean))

  function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!canWrite) return
    const clean = Object.fromEntries(
      brandThemes.map((item) => [item, Object.fromEntries(Object.entries(colors[item]).flatMap(([key, value]) => (value ? [[key, value.startsWith("#") ? value : `#${value}`]] : [])))])
    ) as BrandColors
    if (brandThemes.some((item) => Object.values(clean[item]).some((v) => !hexColor.test(v)))) {
      return setError(t("admin.branding.invalidHex"))
    }
    setError("")
    setColors(clean)
    save.mutate({ ...settingsOf(branding), colors: clean }, { onSuccess: () => toast.success(t("admin.branding.colorsSavedToast")) })
  }

  return (
    <form noValidate className={card} onSubmit={submit}>
      <SectionHeader
        title={t("admin.branding.colorsTitle")}
        description={t("admin.branding.colorsDescription")}
        actions={
          <SegmentedControl
            label={t("admin.branding.theme")}
            options={[
              { value: "dark", label: t("admin.branding.dark") },
              { value: "light", label: t("admin.branding.light") },
            ]}
            value={theme}
            onChange={setTheme}
          />
        }
      />
      <div className="grid gap-6 lg:grid-cols-2">
        <div className="flex flex-col divide-y divide-border">
          {brandColorFields.map(({ key }) => {
            const label = t(`admin.branding.colorFields.${key}`)
            const value = values[key] ?? ""
            const fallback = aetherColors[theme][key]
            return (
              <div key={key} className="flex items-center justify-between gap-4 py-3 first:pt-0 last:pb-0">
                <Label htmlFor={`brand-${key}`}>{label}</Label>
                <div className="flex items-center gap-2">
                  <input
                    type="color"
                    disabled={!canWrite}
                    aria-label={t("admin.branding.pickColorAria", { label: label.toLowerCase() })}
                    value={hexColor.test(value) ? value : fallback}
                    onChange={(e) => setColors((c) => ({ ...c, [theme]: { ...c[theme], [key]: e.target.value } }))}
                    className="size-10 shrink-0 cursor-pointer rounded-lg border border-input bg-sunken p-1 disabled:cursor-default disabled:opacity-60 max-md:size-11 [&::-moz-color-swatch]:rounded-sm [&::-moz-color-swatch]:border-0 [&::-webkit-color-swatch]:rounded-sm [&::-webkit-color-swatch]:border-0 [&::-webkit-color-swatch-wrapper]:p-0"
                  />
                  <Input
                    id={`brand-${key}`}
                    value={value}
                    readOnly={!canWrite}
                    placeholder={fallback}
                    maxLength={7}
                    spellCheck={false}
                    autoComplete="off"
                    aria-invalid={!!value && !hexColor.test(value.startsWith("#") ? value : `#${value}`)}
                    className="w-28 font-mono text-sm"
                    onChange={(e) => setColors((c) => ({ ...c, [theme]: { ...c[theme], [key]: e.target.value.trim().toLowerCase() } }))}
                  />
                </div>
              </div>
            )
          })}
        </div>
        <ColourPreview theme={theme} colors={values} />
      </div>
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {canWrite && (
        <div className="flex flex-wrap gap-3">
          <Button type="submit" variant="secondary" loading={save.isPending}>
            {t("admin.branding.saveColors")}
          </Button>
          <Button type="button" variant="quiet" disabled={!customized} onClick={() => setColors({ dark: {}, light: {} })}>
            {t("admin.branding.resetColors")}
          </Button>
        </div>
      )}
    </form>
  )
}

function ColourPreview({ theme, colors }: { theme: BrandTheme; colors: BrandColors[BrandTheme] }) {
  const { t } = useI18n()
  const valid = Object.fromEntries(Object.entries(colors).filter(([, v]) => v && hexColor.test(v)))

  return (
    <div aria-hidden className={cn(theme, "flex min-h-72 overflow-hidden rounded-lg border border-border bg-background text-foreground")} style={brandVariables(valid) as React.CSSProperties}>
      <div className="flex w-20 shrink-0 flex-col gap-1 border-r border-border bg-card py-3">
        {[
          { label: t("nav.mail"), icon: Mail, active: true },
          { label: t("nav.calendar"), icon: CalendarDays, active: false },
        ].map(({ label, icon: Icon, active }) => (
          <span
            key={label}
            className={cn(
              "relative flex flex-col items-center gap-1 py-2 text-xs font-medium text-fg-tertiary",
              active && "text-foreground before:absolute before:inset-y-1 before:left-0 before:w-0.5 before:bg-ember"
            )}
          >
            <Icon className={cn("size-5", active && "text-ember")} />
            {label}
          </span>
        ))}
      </div>
      <div className="flex min-w-0 flex-1 flex-col p-4">
        <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4">
          <span className="flex items-center gap-2 font-semibold">
            <span className="size-2 shrink-0 rounded-full bg-ember" />
            {t("admin.branding.preview.subject")}
          </span>
          <p className="text-sm text-fg-secondary">
            {t("admin.branding.preview.body")} <span className="text-link underline underline-offset-3">{t("admin.branding.preview.link")}</span>
          </p>
          <Input tabIndex={-1} readOnly value={t("admin.branding.preview.reply")} />
          <div className="flex flex-wrap gap-2">
            <Button type="button" size="sm" tabIndex={-1}>
              {t("admin.branding.preview.send")}
            </Button>
            <Button type="button" size="sm" variant="ghost" tabIndex={-1}>
              {t("admin.branding.preview.schedule")}
            </Button>
            <Button type="button" size="sm" variant="secondary" tabIndex={-1}>
              {t("admin.branding.preview.discard")}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

function CustomCSS({ orgId, branding, canWrite }: { orgId: string; branding: OrgBranding; canWrite: boolean }) {
  const { t } = useI18n()
  const [css, setCss] = useState(branding.customCss)
  const save = useSaveBranding(orgId)
  const size = new Blob([css]).size

  return (
    <form
      noValidate
      className={card}
      onSubmit={(e) => {
        e.preventDefault()
        if (!canWrite) return
        save.mutate(
          { ...settingsOf(branding), customCss: css },
          { onSuccess: () => toast.success(css.trim() ? t("admin.branding.cssSavedToast") : t("admin.branding.cssRemovedToast")) }
        )
      }}
    >
      <SectionHeader title={t("admin.branding.customCssTitle")} description={t("admin.branding.customCssDescription")} />
      {!branding.customCssEnabled && (
        <div role="status" className="flex gap-3 rounded-lg border border-warning/40 bg-warning/12 p-4 text-sm">
          <CircleAlert aria-hidden className="size-4 shrink-0 translate-y-0.5 text-warning" />
          <span>
            {t("admin.branding.customCssDisabledBefore")}
            <code className="font-mono">AETHER_CUSTOM_CSS=false</code>
            {t("admin.branding.customCssDisabledAfter")}
          </span>
        </div>
      )}
      <Field id="brand-css" label={t("admin.branding.stylesheet")} hint={t("admin.branding.stylesheetHint", { size: formatBytes(size) })}>
        <Textarea id="brand-css" rows={12} spellCheck={false} readOnly={!canWrite} value={css} aria-invalid={size > 32 << 10} className="font-mono text-sm" onChange={(e) => setCss(e.target.value)} />
      </Field>
      {canWrite && (
        <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={css === branding.customCss}>
          {t("admin.branding.saveCss")}
        </Button>
      )}
    </form>
  )
}

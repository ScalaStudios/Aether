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
import { api } from "@/lib/api"
import { aetherColors, brandColorFields, brandThemes, brandVariables } from "@/lib/branding"
import { formatBytes } from "@/lib/format"
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
  const branding = useQuery({ queryKey: ["org-branding", orgId], queryFn: () => api<OrgBranding>(`/organizations/${orgId}/branding`) })

  return (
    <QueryView query={branding} skeleton={<RowsSkeleton rows={4} />}>
      {(data) => (
        <div className="flex flex-col gap-6">
          <NameAndLogo orgId={orgId} branding={data} />
          <SignInMessage orgId={orgId} branding={data} />
          <Colours orgId={orgId} branding={data} />
          <CustomCSS orgId={orgId} branding={data} />
        </div>
      )}
    </QueryView>
  )
}

function NameAndLogo({ orgId, branding }: { orgId: string; branding: OrgBranding }) {
  const orgName = useQuery({ ...organizationsQuery, select: (orgs) => orgs.find((o) => o.id === orgId)?.name ?? "" }).data ?? ""
  const [name, setName] = useState(branding.name)
  const save = useSaveBranding(orgId)

  return (
    <section className={card}>
      <SectionHeader title="Name and logo" description="Shown instead of Aether's in the app header, the browser tab and on the sign-in page, for everyone in this organization." />
      <form
        noValidate
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate({ ...settingsOf(branding), name: name.trim() }, { onSuccess: () => toast.success("Name saved") })
        }}
      >
        <Field id="brand-name" label="Name" hint={orgName && `Leave empty to use the organization name, “${orgName}”.`}>
          <div className="flex flex-col gap-3 sm:flex-row">
            <Input id="brand-name" value={name} maxLength={100} autoComplete="off" placeholder={orgName} className="sm:max-w-sm" onChange={(e) => setName(e.target.value)} />
            <Button type="submit" variant="secondary" loading={save.isPending} disabled={name.trim() === branding.name}>
              Save name
            </Button>
          </div>
        </Field>
      </form>
      <div className="grid gap-6 pt-2 sm:grid-cols-3">
        <ImageSlot orgId={orgId} kind="logo-light" label="Light theme logo" url={branding.logoLight} theme="light" />
        <ImageSlot orgId={orgId} kind="logo-dark" label="Dark theme logo" url={branding.logoDark} theme="dark" />
        <ImageSlot orgId={orgId} kind="favicon" label="Favicon" url={branding.favicon} />
      </div>
      <p className="text-sm text-fg-tertiary">
        PNG, JPEG or WebP, up to 256 KB and 2048 by 2048 pixels each. Logos fit within 80 by 32 pixels in the sidebar and 240 by 48 on the sign-in page, and one logo is used for both themes until you add the other. The light theme logo also goes into signatures made from the organization template. Use a square image for the favicon.
      </p>
    </section>
  )
}

function ImageSlot({ orgId, kind, label, url, theme }: { orgId: string; kind: string; label: string; url: string | null; theme?: BrandTheme }) {
  const qc = useQueryClient()
  const input = useRef<HTMLInputElement>(null)
  const done = (saved: OrgBranding, message: string) => {
    qc.setQueryData(["org-branding", orgId], saved)
    qc.invalidateQueries({ queryKey: ["branding"] })
    toast.success(message)
  }
  const upload = useMutation({
    mutationFn: (file: File) => api<OrgBranding>(`/organizations/${orgId}/branding/${kind}`, { method: "PUT", body: file }),
    onSuccess: (saved) => done(saved, `${label} uploaded`),
  })
  const remove = useMutation({
    mutationFn: () => api<OrgBranding>(`/organizations/${orgId}/branding/${kind}`, { method: "DELETE" }),
    onSuccess: (saved) => done(saved, `${label} removed`),
  })

  return (
    <div className="flex flex-col gap-3">
      <span className="text-label text-fg-secondary">{label}</span>
      <div className={cn(theme, "grid h-28 place-items-center rounded-lg border border-border bg-background p-4")}>
        {url ? (
          <img src={url} alt={`Current ${label.toLowerCase()}`} className={kind === "favicon" ? "size-8" : "max-h-12 max-w-full object-contain"} />
        ) : (
          <span className="text-sm text-fg-tertiary">None yet</span>
        )}
      </div>
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
        <Button type="button" variant="secondary" size="sm" aria-label={`${url ? "Replace" : "Upload"} ${label.toLowerCase()}`} loading={upload.isPending} onClick={() => input.current?.click()}>
          <ImageUp aria-hidden />
          {url ? "Replace" : "Upload"}
        </Button>
        {url && (
          <Button type="button" variant="quiet" size="sm" aria-label={`Remove ${label.toLowerCase()}`} loading={remove.isPending} onClick={() => remove.mutate()}>
            Remove
          </Button>
        )}
      </div>
    </div>
  )
}

function SignInMessage({ orgId, branding }: { orgId: string; branding: OrgBranding }) {
  const [message, setMessage] = useState(branding.signInMessage)
  const save = useSaveBranding(orgId)

  return (
    <form
      noValidate
      className={card}
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate({ ...settingsOf(branding), signInMessage: message.trim() }, { onSuccess: () => toast.success(message.trim() ? "Sign-in message saved" : "Sign-in message removed") })
      }}
    >
      <SectionHeader title="Sign-in message" description="Replaces “Sign in to read your mail and manage your organization.” when the sign-in page is opened on one of this organization's domains." />
      <Field id="brand-message" label="Message" hint={`${message.length} of 500 characters. Plain text; line breaks are kept.`}>
        <Textarea
          id="brand-message"
          rows={4}
          maxLength={500}
          value={message}
          placeholder="Sign in with your work account. Locked out? Call the IT desk on extension 4000."
          onChange={(e) => setMessage(e.target.value)}
        />
      </Field>
      <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={message.trim() === branding.signInMessage}>
        Save message
      </Button>
    </form>
  )
}

function Colours({ orgId, branding }: { orgId: string; branding: OrgBranding }) {
  const { resolvedTheme } = useTheme()
  const [theme, setTheme] = useState<BrandTheme>(resolvedTheme === "light" ? "light" : "dark")
  const [colors, setColors] = useState<BrandColors>(branding.colors)
  const [error, setError] = useState("")
  const save = useSaveBranding(orgId)
  const values = colors[theme]
  const customized = brandThemes.some((t) => Object.values(colors[t]).some(Boolean))

  function submit(e: React.FormEvent) {
    e.preventDefault()
    const clean = Object.fromEntries(
      brandThemes.map((t) => [t, Object.fromEntries(Object.entries(colors[t]).flatMap(([key, value]) => (value ? [[key, value.startsWith("#") ? value : `#${value}`]] : [])))])
    ) as BrandColors
    if (brandThemes.some((t) => Object.values(clean[t]).some((v) => !hexColor.test(v)))) {
      return setError("Enter each colour as a six-digit hex value, such as #fa7e26.")
    }
    setError("")
    setColors(clean)
    save.mutate({ ...settingsOf(branding), colors: clean }, { onSuccess: () => toast.success("Colours saved") })
  }

  return (
    <form noValidate className={card} onSubmit={submit}>
      <SectionHeader
        title="Colours"
        description="Each theme has its own colours. Tints and the text on accent buttons follow the accent; hover the buttons in the preview to check the hover and pressed colours."
        actions={<SegmentedControl label="Theme" options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} value={theme} onChange={setTheme} />}
      />
      <div className="grid gap-6 lg:grid-cols-2">
        <div className="flex flex-col divide-y divide-border">
          {brandColorFields.map(({ key, label }) => {
            const value = values[key] ?? ""
            const fallback = aetherColors[theme][key]
            return (
              <div key={key} className="flex items-center justify-between gap-4 py-3 first:pt-0 last:pb-0">
                <Label htmlFor={`brand-${key}`}>{label}</Label>
                <div className="flex items-center gap-2">
                  <input
                    type="color"
                    aria-label={`Pick the ${label.toLowerCase()} colour`}
                    value={hexColor.test(value) ? value : fallback}
                    onChange={(e) => setColors((c) => ({ ...c, [theme]: { ...c[theme], [key]: e.target.value } }))}
                    className="size-10 shrink-0 cursor-pointer rounded-lg border border-input bg-sunken p-1 max-md:size-11 [&::-moz-color-swatch]:rounded-sm [&::-moz-color-swatch]:border-0 [&::-webkit-color-swatch]:rounded-sm [&::-webkit-color-swatch]:border-0 [&::-webkit-color-swatch-wrapper]:p-0"
                  />
                  <Input
                    id={`brand-${key}`}
                    value={value}
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
      <div className="flex flex-wrap gap-3">
        <Button type="submit" variant="secondary" loading={save.isPending}>
          Save colours
        </Button>
        <Button type="button" variant="quiet" disabled={!customized} onClick={() => setColors({ dark: {}, light: {} })}>
          Reset to Aether colours
        </Button>
      </div>
    </form>
  )
}

function ColourPreview({ theme, colors }: { theme: BrandTheme; colors: BrandColors[BrandTheme] }) {
  const valid = Object.fromEntries(Object.entries(colors).filter(([, v]) => v && hexColor.test(v)))

  return (
    <div aria-hidden className={cn(theme, "flex min-h-72 overflow-hidden rounded-lg border border-border bg-background text-foreground")} style={brandVariables(valid) as React.CSSProperties}>
      <div className="flex w-20 shrink-0 flex-col gap-1 border-r border-border bg-card py-3">
        {[
          { label: "Mail", icon: Mail, active: true },
          { label: "Calendar", icon: CalendarDays, active: false },
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
            Quarterly planning
          </span>
          <p className="text-sm text-fg-secondary">
            The agenda is ready. <span className="text-link underline underline-offset-3">Open the agenda</span>
          </p>
          <Input tabIndex={-1} readOnly value="Thanks, see you there." />
          <div className="flex flex-wrap gap-2">
            <Button type="button" size="sm" tabIndex={-1}>
              Send
            </Button>
            <Button type="button" size="sm" variant="ghost" tabIndex={-1}>
              Schedule
            </Button>
            <Button type="button" size="sm" variant="secondary" tabIndex={-1}>
              Discard
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

function CustomCSS({ orgId, branding }: { orgId: string; branding: OrgBranding }) {
  const [css, setCss] = useState(branding.customCss)
  const save = useSaveBranding(orgId)
  const size = new Blob([css]).size

  return (
    <form
      noValidate
      className={card}
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate({ ...settingsOf(branding), customCss: css }, { onSuccess: () => toast.success(css.trim() ? "Custom CSS saved" : "Custom CSS removed") })
      }}
    >
      <SectionHeader
        title="Custom CSS"
        description="Added after Aether's styles on the sign-in page, Mail, Calendar, Contacts and Settings, for what the colours don't cover. Administration keeps Aether's styles, so a broken stylesheet can't lock you out of fixing it."
      />
      {!branding.customCssEnabled && (
        <div role="status" className="flex gap-3 rounded-lg border border-warning/40 bg-warning/12 p-4 text-sm">
          <CircleAlert aria-hidden className="size-4 shrink-0 translate-y-0.5 text-warning" />
          <span>
            Custom CSS is turned off on this server, so the stylesheet below isn&apos;t applied. You can still edit it. The operator turns it back on by removing <code className="font-mono">AETHER_CUSTOM_CSS=false</code>.
          </span>
        </div>
      )}
      <Field id="brand-css" label="Stylesheet" hint={`${formatBytes(size)} of 32 KB. url() can only point at files on this server, and @import isn't allowed. Every change is recorded in the audit log.`}>
        <Textarea id="brand-css" rows={12} spellCheck={false} value={css} aria-invalid={size > 32 << 10} className="font-mono text-sm" onChange={(e) => setCss(e.target.value)} />
      </Field>
      <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={css === branding.customCss}>
        Save CSS
      </Button>
    </form>
  )
}

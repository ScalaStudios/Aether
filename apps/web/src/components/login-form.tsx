"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery } from "@tanstack/react-query"
import { CircleX, Globe, KeyRound } from "lucide-react"
import Image from "next/image"
import { useRouter, useSearchParams } from "next/navigation"
import { useState } from "react"
import { useMemo } from "react"
import { useForm } from "react-hook-form"
import { z } from "zod"

import { Field } from "@/components/field"
import { ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api, ApiError, apiUrl, errorMessage } from "@/lib/api"
import { LOCALE_LABELS, SUPPORTED_LOCALES, type Locale, useI18n } from "@/lib/i18n"
import { brandingQuery } from "@/lib/queries"
import type { AuthProviders, LoginResult, Session, TwoFactorPrompt } from "@/lib/types"
import { getSecurityKey, securityKeysSupported } from "@/lib/webauthn"

const knownLoginErrors = new Set([
  "rate_limited",
  "unknown_provider",
  "provider_unavailable",
  "provider_denied",
  "state_mismatch",
  "state_expired",
  "exchange_failed",
  "invalid_token",
  "email_unverified",
  "not_provisioned",
  "account_disabled",
  "limit_reached",
  "shared_mailbox",
])

type Values = { email: string; password: string }

export function LoginForm() {
  const { t } = useI18n()
  const params = useSearchParams()
  const router = useRouter()
  const errorCode = params.get("error")
  const providers = useQuery({
    queryKey: ["auth-providers"],
    queryFn: () => api<AuthProviders>("/auth/providers", { query: { host: window.location.hostname } }),
  })
  const branding = useQuery(brandingQuery)
  const schema = useMemo(
    () =>
      z.object({
        email: z.email(t("login.validation.email")),
        password: z.string().min(1, t("login.validation.password")),
      }),
    [t]
  )
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { email: "", password: "" } })
  const [prompt, setPrompt] = useState<TwoFactorPrompt | null>(null)
  const [restartMessage, setRestartMessage] = useState("")
  const login = useMutation({
    mutationFn: (values: Values) => api<LoginResult>("/auth/login", { method: "POST", body: values }),
    meta: { silent: true },
    onSuccess: (result) => {
      if ("twoFactor" in result) {
        setRestartMessage("")
        setPrompt(result.twoFactor)
      } else router.replace("/mail")
    },
  })

  const message = login.isError
    ? errorMessage(login.error)
    : restartMessage ||
      (errorCode &&
        (knownLoginErrors.has(errorCode) ? t(`login.errors.${errorCode}`) : t("login.errors.fallback", { code: errorCode })))

  if (providers.isPending || branding.isPending) return <RowsSkeleton rows={3} />
  if (providers.isError) return <ErrorState error={providers.error} onRetry={() => providers.refetch()} />

  const { localLogin, providers: list } = providers.data

  if (prompt) {
    return (
      <TwoFactorStep
        prompt={prompt}
        onRestart={(reason) => {
          setPrompt(null)
          setRestartMessage(reason)
          login.reset()
          form.resetField("password")
        }}
      />
    )
  }

  return (
    <div className="animate-content flex flex-col gap-6">
      <p className="whitespace-pre-line text-fg-secondary">{branding.data?.signInMessage || t("login.defaultMessage")}</p>
      {message && <ErrorAlert>{message}</ErrorAlert>}
      {list.length > 0 && (
        <div className="flex flex-col gap-3">
          {list.map((p) => (
            <Button key={p.slug} variant={localLogin ? "secondary" : "default"} asChild>
              <a href={apiUrl(`/auth/oidc/${encodeURIComponent(p.slug)}/start`, { return_to: "/mail" })}>
                <KeyRound aria-hidden />
                {p.kind === "halo" ? t("login.continueWith", { name: p.name }) : t("login.signInWith", { name: p.name })}
              </a>
            </Button>
          ))}
        </div>
      )}
      {list.length > 0 && localLogin && (
        <div className="flex items-center gap-3 text-xs text-fg-tertiary">
          <span className="h-px flex-1 bg-border" />
          {t("common.or")}
          <span className="h-px flex-1 bg-border" />
        </div>
      )}
      {localLogin && (
        <form onSubmit={form.handleSubmit((v) => login.mutate(v))} noValidate className="flex flex-col gap-4">
          <Field id="email" label={t("login.email")} error={form.formState.errors.email?.message}>
            <Input id="email" type="email" autoComplete="username" aria-invalid={!!form.formState.errors.email} {...form.register("email")} />
          </Field>
          <Field id="password" label={t("login.password")} error={form.formState.errors.password?.message}>
            <Input id="password" type="password" autoComplete="current-password" aria-invalid={!!form.formState.errors.password} {...form.register("password")} />
          </Field>
          <Button type="submit" loading={login.isPending || login.isSuccess} className="mt-2">
            {t("login.signIn")}
          </Button>
        </form>
      )}
      {!localLogin && list.length === 0 && <p className="text-fg-secondary">{t("login.noMethodConfigured")}</p>}
    </div>
  )
}

export function LoginFooter() {
  const { t, locale, setGuestLocale } = useI18n()

  return (
    <div className="flex flex-col items-center gap-4">
      <div className="flex items-center gap-2">
        <Globe aria-hidden className="size-4 text-fg-tertiary" />
        <Select value={locale} onValueChange={(value) => setGuestLocale(value as Locale)}>
          <SelectTrigger aria-label={t("login.languageLabel")} className="h-9 w-auto gap-2 border-border bg-transparent px-3 text-sm text-fg-secondary">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SUPPORTED_LOCALES.map((code) => (
              <SelectItem key={code} value={code}>
                {LOCALE_LABELS[code]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <p className="flex items-center gap-2 text-sm text-fg-tertiary">
        <Image src="/brand/scala-symbol-gradient.svg" alt="" width={24} height={31} className="h-[31px] w-6 light:hidden" />
        <Image src="/brand/scala-symbol-ink.svg" alt="" width={24} height={31} className="hidden h-[31px] w-6 light:block" />
        {t("login.footer")}
      </p>
    </div>
  )
}

function ErrorAlert({ children }: { children: React.ReactNode }) {
  return (
    <div role="alert" className="flex gap-3 rounded-lg border border-danger/40 bg-danger/12 p-4 text-sm">
      <CircleX aria-hidden className="size-4 shrink-0 translate-y-0.5 text-danger" />
      <span>{children}</span>
    </div>
  )
}

function TwoFactorStep({ prompt, onRestart }: { prompt: TwoFactorPrompt; onRestart: (reason: string) => void }) {
  const router = useRouter()
  const [mode, setMode] = useState<"totp" | "recovery">(prompt.totp ? "totp" : "recovery")
  const [code, setCode] = useState("")
  const securityKey = prompt.securityKey && securityKeysSupported() ? prompt.securityKey : null
  const verify = useMutation({
    mutationFn: async (body: { code?: string; recoveryCode?: string; securityKey?: true }) =>
      api<Session>("/auth/login/two-factor", { method: "POST", body: body.securityKey ? { securityKey: await getSecurityKey(securityKey!) } : body }),
    meta: { silent: true },
    onSuccess: () => router.replace("/mail"),
    onError: (error) => {
      if (error instanceof ApiError && error.code === "two_factor_expired") onRestart(error.message)
    },
  })
  const recovery = mode === "recovery"

  return (
    <div className="animate-content flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <h2 className="text-h3">Two-factor authentication</h2>
        <p className="text-fg-secondary">
          {securityKey ? "Use your security key or passkey" : recovery ? "Enter one of your recovery codes" : "Enter the code from your authenticator app"} to finish signing in.
        </p>
      </div>
      {verify.isError && <ErrorAlert>{errorMessage(verify.error)}</ErrorAlert>}
      {securityKey && (
        <>
          <Button autoFocus loading={verify.isPending && verify.variables?.securityKey} disabled={verify.isPending || verify.isSuccess} onClick={() => verify.mutate({ securityKey: true })}>
            <KeyRound aria-hidden />
            Use security key or passkey
          </Button>
          <div className="flex items-center gap-3 text-xs text-fg-tertiary">
            <span className="h-px flex-1 bg-border" />
            or
            <span className="h-px flex-1 bg-border" />
          </div>
        </>
      )}
      <form
        noValidate
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (code.trim()) verify.mutate(recovery ? { recoveryCode: code.trim() } : { code: code.replace(/\s/g, "") })
        }}
      >
        <Field id="two-factor-code" label={recovery ? "Recovery code" : "Authentication code"} hint={recovery ? "Each recovery code works once." : "The 6-digit code your authenticator app shows for Aether."}>
          <Input
            key={mode}
            id="two-factor-code"
            autoFocus={!securityKey}
            autoComplete={recovery ? "off" : "one-time-code"}
            inputMode={recovery ? "text" : "numeric"}
            spellCheck={false}
            maxLength={recovery ? 24 : 7}
            className="font-mono"
            value={code}
            onChange={(e) => setCode(e.target.value)}
          />
        </Field>
        <Button type="submit" variant={securityKey ? "secondary" : "default"} disabled={!code.trim() || verify.isSuccess} loading={verify.isPending && !verify.variables?.securityKey}>
          Verify
        </Button>
      </form>
      <div className="flex flex-wrap justify-between gap-2">
        {prompt.totp && (
          <Button
            variant="link"
            onClick={() => {
              setMode(recovery ? "totp" : "recovery")
              setCode("")
              verify.reset()
            }}
          >
            {recovery ? "Use your authenticator app" : "Use a recovery code"}
          </Button>
        )}
        <Button variant="link" onClick={() => onRestart("")}>
          Start over
        </Button>
      </div>
    </div>
  )
}

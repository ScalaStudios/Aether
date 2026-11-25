"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery } from "@tanstack/react-query"
import { CircleX, KeyRound } from "lucide-react"
import { useRouter, useSearchParams } from "next/navigation"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { z } from "zod"

import { Field } from "@/components/field"
import { ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api, ApiError, apiUrl, errorMessage } from "@/lib/api"
import { brandingQuery } from "@/lib/queries"
import type { AuthProviders, LoginResult, Session, TwoFactorPrompt } from "@/lib/types"
import { getSecurityKey, securityKeysSupported } from "@/lib/webauthn"

const loginErrors: Record<string, string> = {
  rate_limited: "Too many sign-in attempts. Wait a minute and try again.",
  unknown_provider: "That sign-in provider is not available. Choose another sign-in method.",
  provider_unavailable: "The identity provider could not be reached. Try again in a few minutes.",
  provider_denied: "The identity provider did not approve the sign-in. Try again, or contact your administrator.",
  state_mismatch: "The sign-in request could not be matched to this browser. Start the sign-in again from this page.",
  state_expired: "The sign-in request expired. Start the sign-in again.",
  exchange_failed: "Aether could not complete the sign-in with the identity provider. Try again.",
  invalid_token: "The identity provider returned an invalid response. Contact your administrator.",
  email_unverified: "Your identity provider has not verified your email address. Verify it there, then try again.",
  not_provisioned: "Your account does not have access to Aether yet. Ask your administrator to add you.",
  account_disabled: "This account is suspended. Contact your administrator.",
  shared_mailbox: "This address is a shared mailbox and cannot sign in. Sign in with your own account; the shared mailbox appears in your sidebar once an administrator adds you.",
}

const schema = z.object({
  email: z.email("Enter a valid email address."),
  password: z.string().min(1, "Enter your password."),
})

type Values = z.infer<typeof schema>

export function LoginForm() {
  const params = useSearchParams()
  const router = useRouter()
  const errorCode = params.get("error")
  const providers = useQuery({ queryKey: ["auth-providers"], queryFn: () => api<AuthProviders>("/auth/providers") })
  const branding = useQuery(brandingQuery)
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
    : restartMessage || (errorCode && (loginErrors[errorCode] ?? `Sign-in failed (${errorCode}). Try again, or contact your administrator.`))

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
      <p className="whitespace-pre-line text-fg-secondary">{branding.data?.signInMessage || "Sign in to read your mail and manage your organization."}</p>
      {message && <ErrorAlert>{message}</ErrorAlert>}
      {list.length > 0 && (
        <div className="flex flex-col gap-3">
          {list.map((p) => (
            <Button key={p.slug} variant={localLogin ? "secondary" : "default"} asChild>
              <a href={apiUrl(`/auth/oidc/${encodeURIComponent(p.slug)}/start`, { return_to: "/mail" })}>
                <KeyRound aria-hidden />
                {p.kind === "halo" ? `Continue with ${p.name}` : `Sign in with ${p.name}`}
              </a>
            </Button>
          ))}
        </div>
      )}
      {list.length > 0 && localLogin && (
        <div className="flex items-center gap-3 text-xs text-fg-tertiary">
          <span className="h-px flex-1 bg-border" />
          or
          <span className="h-px flex-1 bg-border" />
        </div>
      )}
      {localLogin && (
        <form onSubmit={form.handleSubmit((v) => login.mutate(v))} noValidate className="flex flex-col gap-4">
          <Field id="email" label="Email" error={form.formState.errors.email?.message}>
            <Input id="email" type="email" autoComplete="username" aria-invalid={!!form.formState.errors.email} {...form.register("email")} />
          </Field>
          <Field id="password" label="Password" error={form.formState.errors.password?.message}>
            <Input id="password" type="password" autoComplete="current-password" aria-invalid={!!form.formState.errors.password} {...form.register("password")} />
          </Field>
          <Button type="submit" loading={login.isPending || login.isSuccess} className="mt-2">
            Sign in
          </Button>
        </form>
      )}
      {!localLogin && list.length === 0 && (
        <p className="text-fg-secondary">No sign-in method is configured on this installation. Ask the operator to enable password sign-in or add an identity provider.</p>
      )}
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

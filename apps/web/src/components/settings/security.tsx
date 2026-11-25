"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Download, KeyRound, Trash2 } from "lucide-react"
import Image from "next/image"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { CopyButton, CopyField } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { ErrorState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { DialogClose } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { api, ApiError, errorMessage } from "@/lib/api"
import { formatDate, plural } from "@/lib/format"
import type { SecurityKey, SecurityStatus, TOTPSetup } from "@/lib/types"
import { createSecurityKey, securityKeysSupported, type SecurityKeyCreationOptions } from "@/lib/webauthn"

const card = "flex flex-col gap-4 rounded-xl border border-border bg-card p-6"
const row = "flex flex-col gap-3 border-t border-border pt-4 sm:flex-row sm:items-start sm:justify-between"

type RecoveryCodes = { recoveryCodes: string[] }

function useSecurityChanged() {
  const qc = useQueryClient()
  return () => {
    qc.invalidateQueries({ queryKey: ["security"] })
    qc.invalidateQueries({ queryKey: ["session"] })
  }
}

export function Security({ setupRequired }: { setupRequired: boolean }) {
  const status = useQuery({ queryKey: ["security"], queryFn: () => api<SecurityStatus>("/me/security") })

  return (
    <section id="security" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader title="Security" description="Your Aether password, the second step when you sign in with it, and recovery codes for when your phone or security key is not at hand." />
      <QueryView query={status} skeleton={<RowsSkeleton rows={3} />}>
        {(data) =>
          data.passwordSignIn ? (
            <div className="flex flex-col gap-6">
              {setupRequired && (
                <p role="status" className="rounded-lg border border-warning/40 bg-warning/12 p-4 text-sm">
                  Your organization requires two-factor authentication. Set up an authenticator app or a security key to keep using Aether.
                </p>
              )}
              <Password />
              <TwoFactor status={data} />
            </div>
          ) : (
            <p className={`${card} text-fg-secondary`}>You sign in through your identity provider, so your password and two-factor authentication are managed there.</p>
          )
        }
      </QueryView>
    </section>
  )
}

const passwordSchema = z
  .object({
    currentPassword: z.string().min(1, "Enter your current password."),
    newPassword: z.string().min(12, "Use at least 12 characters."),
    confirmPassword: z.string(),
  })
  .refine((v) => v.newPassword === v.confirmPassword, { path: ["confirmPassword"], message: "The passwords do not match." })

type PasswordValues = z.infer<typeof passwordSchema>

function Password() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const form = useForm<PasswordValues>({ resolver: zodResolver(passwordSchema), defaultValues: { currentPassword: "", newPassword: "", confirmPassword: "" } })
  const change = useMutation({
    mutationFn: ({ currentPassword, newPassword }: PasswordValues) => api("/me/password", { method: "PUT", body: { currentPassword, newPassword } }),
    meta: { silent: true },
    onSuccess: () => {
      toast.success("Password changed", { description: "Your other sessions were signed out." })
      qc.invalidateQueries({ queryKey: ["sessions"] })
      close(false)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "wrong_password") form.setError("currentPassword", { message: error.message })
      else if (error instanceof ApiError && error.code === "weak_password") form.setError("newPassword", { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const errors = form.formState.errors

  function close(next: boolean) {
    setOpen(next)
    if (!next) form.reset()
  }

  return (
    <div className={card}>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex flex-col gap-1">
          <h3 className="font-semibold">Password</h3>
          <p className="text-sm text-fg-tertiary">Changing it signs you out everywhere except this browser. App passwords for mail apps keep working.</p>
        </div>
        <Button variant="secondary" className="self-start" onClick={() => setOpen(true)}>
          Change password
        </Button>
      </div>
      <FormDialog open={open} onOpenChange={close} title="Change password" submitLabel="Change password" pending={change.isPending} onSubmit={form.handleSubmit((v) => change.mutate(v))}>
        <Field id="current-password" label="Current password" error={errors.currentPassword?.message}>
          <Input id="current-password" type="password" autoComplete="current-password" aria-invalid={!!errors.currentPassword} {...form.register("currentPassword")} />
        </Field>
        <Field id="new-password" label="New password" hint="At least 12 characters." error={errors.newPassword?.message}>
          <Input id="new-password" type="password" autoComplete="new-password" aria-invalid={!!errors.newPassword} {...form.register("newPassword")} />
        </Field>
        <Field id="confirm-password" label="Confirm new password" error={errors.confirmPassword?.message}>
          <Input id="confirm-password" type="password" autoComplete="new-password" aria-invalid={!!errors.confirmPassword} {...form.register("confirmPassword")} />
        </Field>
      </FormDialog>
    </div>
  )
}

type Removal = { kind: "totp" } | { kind: "key"; key: SecurityKey } | { kind: "codes" }

function TwoFactor({ status }: { status: SecurityStatus }) {
  const enabled = status.totp || status.securityKeys.length > 0
  const [confirming, setConfirming] = useState<Removal | null>(null)

  return (
    <div className={card}>
      <div className="flex flex-col gap-1">
        <h3 className="flex flex-wrap items-center gap-2 font-semibold">
          Two-factor authentication
          <StatusBadge tone={enabled ? "success" : status.twoFactorRequired ? "warning" : "neutral"}>{enabled ? "On" : "Off"}</StatusBadge>
        </h3>
        <p className="text-sm text-fg-tertiary">
          After your password, Aether asks for a code from your phone or a tap on your security key.
          {status.twoFactorRequired && " Your organization requires it."}
        </p>
      </div>
      <div className={row}>
        <div className="flex flex-col gap-1">
          <span className="font-medium">Authenticator app</span>
          <span className="text-sm text-fg-tertiary">{status.totp ? "Set up. Aether asks for the 6-digit code it shows." : "Codes from an app such as 1Password, Google Authenticator or Microsoft Authenticator."}</span>
        </div>
        <AuthenticatorSetup enabled={status.totp} onTurnOff={() => setConfirming({ kind: "totp" })} />
      </div>
      <div className={row}>
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="font-medium">Security keys and passkeys</span>
          <span className="text-sm text-fg-tertiary">
            {securityKeysSupported() ? "A hardware key, or a passkey on this device or your phone." : "This browser does not support security keys."}
          </span>
          {status.securityKeys.length > 0 && (
            <ul className="mt-2 flex flex-col">
              {status.securityKeys.map((k) => (
                <li key={k.id} className="flex items-center justify-between gap-3 border-b border-border py-2 last:border-b-0">
                  <div className="flex min-w-0 flex-col">
                    <span className="truncate font-medium">{k.name}</span>
                    <span className="text-sm text-fg-tertiary">
                      Added {formatDate(k.createdAt)}
                      {k.lastUsedAt && ` · last used ${formatDate(k.lastUsedAt)}`}
                    </span>
                  </div>
                  <IconButton label={`Remove ${k.name}`} size="icon-sm" onClick={() => setConfirming({ kind: "key", key: k })}>
                    <Trash2 />
                  </IconButton>
                </li>
              ))}
            </ul>
          )}
        </div>
        {securityKeysSupported() && <SecurityKeySetup />}
      </div>
      {enabled && (
        <div className={row}>
          <div className="flex flex-col gap-1">
            <span className="font-medium">Recovery codes</span>
            <span className="text-sm text-fg-tertiary">{plural(status.recoveryCodes, "unused code")} left. Each one signs you in once without your phone or key.</span>
          </div>
          <Button variant="secondary" className="self-start" onClick={() => setConfirming({ kind: "codes" })}>
            Generate new codes
          </Button>
        </div>
      )}
      <ConfirmWithPassword removal={confirming} onClose={() => setConfirming(null)} />
    </div>
  )
}

function RecoveryCodeList({ codes }: { codes: string[] }) {
  const text = codes.join("\n")
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-fg-secondary">Store these somewhere safe, such as your password manager. Each code signs you in once if your phone or security key is not at hand. Aether shows them only now.</p>
      <ul aria-label="Recovery codes" className="grid grid-cols-1 gap-2 rounded-xl border border-border p-4 font-mono text-sm sm:grid-cols-2">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <div className="flex flex-wrap items-center gap-2">
        <CopyButton value={text} label="Copy recovery codes" />
        <Button variant="secondary" asChild>
          <a href={`data:text/plain;charset=utf-8,${encodeURIComponent(text + "\n")}`} download="aether-recovery-codes.txt">
            <Download aria-hidden />
            Download
          </a>
        </Button>
      </div>
    </div>
  )
}

function DoneFooter() {
  return (
    <DialogClose asChild>
      <Button type="button">Done</Button>
    </DialogClose>
  )
}

function AuthenticatorSetup({ enabled, onTurnOff }: { enabled: boolean; onTurnOff: () => void }) {
  const changed = useSecurityChanged()
  const [open, setOpen] = useState(false)
  const [code, setCode] = useState("")
  const [codes, setCodes] = useState<string[] | null>(null)
  const start = useMutation({ mutationFn: () => api<TOTPSetup>("/me/totp", { method: "POST" }), meta: { silent: true } })
  const confirm = useMutation({
    mutationFn: () => api<RecoveryCodes>("/me/totp", { method: "PUT", body: { code: code.replace(/\s/g, "") } }),
    meta: { silent: true },
    onSuccess: (data) => {
      toast.success("Authenticator app set up")
      changed()
      if (data.recoveryCodes.length) setCodes(data.recoveryCodes)
      else close(false)
    },
  })

  function close(next: boolean) {
    setOpen(next)
    if (!next) {
      setCode("")
      setCodes(null)
      confirm.reset()
    }
  }

  return (
    <>
      {enabled ? (
        <Button variant="secondary" className="self-start" onClick={onTurnOff}>
          Turn off
        </Button>
      ) : (
        <Button
          variant="secondary"
          className="self-start"
          onClick={() => {
            setOpen(true)
            start.mutate()
          }}
        >
          Set up
        </Button>
      )}
      <FormDialog
        open={open}
        onOpenChange={close}
        title={codes ? "Save your recovery codes" : "Set up an authenticator app"}
        description={codes ? undefined : "Scan the QR code with your authenticator app, or enter the setup key by hand, then type the code it shows."}
        submitLabel="Turn on"
        pending={confirm.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (code.trim()) confirm.mutate()
        }}
        footer={codes && <DoneFooter />}
      >
        {codes ? (
          <RecoveryCodeList codes={codes} />
        ) : start.isError ? (
          <ErrorState error={start.error} onRetry={() => start.mutate()} />
        ) : !start.data ? (
          <RowsSkeleton rows={3} />
        ) : (
          <div className="animate-content flex flex-col gap-4">
            <Image src={start.data.qrCode} alt="QR code for your authenticator app" width={192} height={192} unoptimized className="size-48 self-center rounded-lg bg-white p-2 [image-rendering:pixelated]" />
            <CopyField id="totp-secret" label="Setup key" value={start.data.secret.replace(/(.{4})(?=.)/g, "$1 ")} copyLabel="Copy setup key" />
            <Field id="totp-code" label="Code from the app" error={confirm.error ? errorMessage(confirm.error) : undefined}>
              <Input
                id="totp-code"
                autoFocus
                autoComplete="one-time-code"
                inputMode="numeric"
                maxLength={7}
                spellCheck={false}
                className="font-mono"
                aria-invalid={confirm.isError}
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
            </Field>
          </div>
        )}
      </FormDialog>
    </>
  )
}

function SecurityKeySetup() {
  const changed = useSecurityChanged()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [codes, setCodes] = useState<string[] | null>(null)
  const add = useMutation({
    mutationFn: async () => {
      const options = await api<SecurityKeyCreationOptions>("/me/security-keys/challenge", { method: "POST" })
      const credential = await createSecurityKey(options)
      return api<RecoveryCodes>("/me/security-keys", { method: "POST", body: { name: name.trim(), credential } })
    },
    meta: { silent: true },
    onSuccess: (data) => {
      toast.success(`Added ${name.trim()}`)
      changed()
      if (data.recoveryCodes.length) setCodes(data.recoveryCodes)
      else close(false)
    },
  })

  function close(next: boolean) {
    setOpen(next)
    if (!next) {
      setName("")
      setCodes(null)
      add.reset()
    }
  }

  return (
    <>
      <Button variant="secondary" className="self-start" onClick={() => setOpen(true)}>
        <KeyRound aria-hidden />
        Add security key
      </Button>
      <FormDialog
        open={open}
        onOpenChange={close}
        title={codes ? "Save your recovery codes" : "Add a security key"}
        description={codes ? undefined : "Name the key, then follow your browser's prompt to touch the key or confirm with your device."}
        submitLabel="Add key"
        pending={add.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (name.trim()) add.mutate()
        }}
        footer={codes && <DoneFooter />}
      >
        {codes ? (
          <RecoveryCodeList codes={codes} />
        ) : (
          <Field id="key-name" label="Name" hint="For example “YubiKey” or “Work laptop”." error={add.error ? errorMessage(add.error) : undefined}>
            <Input id="key-name" maxLength={100} autoComplete="off" aria-invalid={add.isError} value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
        )}
      </FormDialog>
    </>
  )
}

function ConfirmWithPassword({ removal, onClose }: { removal: Removal | null; onClose: () => void }) {
  const changed = useSecurityChanged()
  const [password, setPassword] = useState("")
  const [codes, setCodes] = useState<string[] | null>(null)
  const confirm = useMutation({
    mutationFn: (r: Removal) =>
      r.kind === "codes"
        ? api<RecoveryCodes>("/me/recovery-codes", { method: "POST", body: { password } })
        : api<undefined>(r.kind === "totp" ? "/me/totp" : `/me/security-keys/${r.key.id}`, { method: "DELETE", body: { password } }),
    meta: { silent: true },
    onSuccess: (data, r) => {
      changed()
      if (data) setCodes(data.recoveryCodes)
      else {
        toast.success(r.kind === "key" ? `Removed ${r.key.name}` : "Authenticator app turned off")
        close()
      }
    },
  })

  function close() {
    onClose()
    setPassword("")
    setCodes(null)
    confirm.reset()
  }

  const text = {
    totp: { title: "Turn off the authenticator app?", description: "Aether stops asking for its codes. If it is your only method, two-factor authentication turns off and your recovery codes stop working.", label: "Turn off" },
    key: { title: `Remove ${removal?.kind === "key" ? removal.key.name : ""}?`, description: "The key can no longer sign you in. If it is your only method, two-factor authentication turns off and your recovery codes stop working.", label: "Remove key" },
    codes: { title: "Generate new recovery codes?", description: "Your current recovery codes stop working.", label: "Generate codes" },
  }[removal?.kind ?? "codes"]

  return (
    <FormDialog
      open={!!removal}
      onOpenChange={(next) => !next && close()}
      title={codes ? "Save your recovery codes" : text.title}
      description={codes ? undefined : text.description}
      submitLabel={text.label}
      pending={confirm.isPending}
      onSubmit={(e) => {
        e.preventDefault()
        if (removal && password) confirm.mutate(removal)
      }}
      footer={codes && <DoneFooter />}
    >
      {codes ? (
        <RecoveryCodeList codes={codes} />
      ) : (
        <Field id="confirm-password-current" label="Current password" error={confirm.error ? errorMessage(confirm.error) : undefined}>
          <Input id="confirm-password-current" type="password" autoComplete="current-password" aria-invalid={confirm.isError} value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
      )}
    </FormDialog>
  )
}

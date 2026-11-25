"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil, Plus, Shuffle, Trash2, VenetianMask } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyButton } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { MailboxPicker } from "@/components/settings/mail-rules"
import { EmptyState, ErrorState } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { api, ApiError, errorMessage } from "@/lib/api"
import { dateValue } from "@/lib/calendar"
import { formatDate } from "@/lib/format"
import { myMailboxesQuery, personalAliasPolicyQuery } from "@/lib/queries"
import type { Alias, Mailbox } from "@/lib/types"

const personalAliasesQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["personal-aliases", orgId],
    queryFn: () => api<{ aliases: Alias[]; keyMailboxId: string | null }>(`/organizations/${orgId}/personal-aliases`),
  })

const schema = z.object({
  localPart: z
    .string()
    .trim()
    .toLowerCase()
    .regex(/^([a-z0-9]([a-z0-9._-]*[a-z0-9])?)?$/, "Use letters, digits, dots, hyphens or underscores, starting and ending with a letter or digit.")
    .refine((v) => !v.includes(".."), "Don’t put two dots in a row."),
  note: z.string().trim().max(1000, "Keep the note to 1,000 characters."),
  expires: z.string(),
})

type Values = z.infer<typeof schema>

const fieldErrors: Record<string, keyof Values> = {
  invalid_local_part: "localPart",
  address_taken: "localPart",
  invalid_note: "note",
  invalid_expiry: "expires",
}

export const expired = (a: Alias) => !!a.expiresAt && new Date(a.expiresAt) <= new Date()

export function PersonalAliases() {
  const mailboxes = useQuery({ ...myMailboxesQuery, select: (all) => all.filter((m) => m.access === "owner" && m.kind === "user") })
  const [chosen, setChosen] = useState<string | null>(null)
  const items = mailboxes.data ?? []
  const mailbox = items.find((m) => m.id === chosen) ?? items[0]
  if (!mailbox) return null
  return <PersonalAliasSection key={mailbox.id} mailbox={mailbox} picker={<MailboxPicker mailboxes={items} value={mailbox.id} onChange={setChosen} />} />
}

function PersonalAliasSection({ mailbox, picker }: { mailbox: Mailbox; picker: React.ReactNode }) {
  const qc = useQueryClient()
  const orgId = mailbox.orgId
  const policy = useQuery(personalAliasPolicyQuery(orgId))
  const list = useQuery(personalAliasesQuery(orgId))
  const [domainChoice, setDomainChoice] = useState("")
  const [editing, setEditing] = useState<Alias | "new" | null>(null)
  const [deleting, setDeleting] = useState<Alias | null>(null)
  const [confirmKey, setConfirmKey] = useState<"replace" | "revoke" | null>(null)
  const [token, setToken] = useState("")
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { localPart: "", note: "", expires: "" } })
  const errors = form.formState.errors

  const domains = policy.data?.domains.filter((d) => d.available) ?? []
  const domain = domains.find((d) => d.id === domainChoice) ?? domains[0]
  const limit = policy.data?.limitPerUser ?? 0
  const all = list.data?.aliases ?? []
  const aliases = all.filter((a) => a.destinations.includes(mailbox.address))
  const keyMailboxId = list.data?.keyMailboxId ?? null
  const open = limit > 0 && !!domain
  const full = all.length >= limit
  const target = { mailboxId: mailbox.id, domainId: domain?.id }

  function refresh() {
    qc.invalidateQueries({ queryKey: ["personal-aliases", orgId] })
    qc.invalidateQueries({ queryKey: ["aliases", orgId] })
  }

  const generate = useMutation({
    mutationFn: () => api<Alias>(`/organizations/${orgId}/personal-aliases`, { method: "POST", body: target }),
    onSuccess: (a) => {
      refresh()
      toast.success(`Created ${a.address}`, { action: { label: "Copy", onClick: () => navigator.clipboard.writeText(a.address).catch(() => toast.error("Copying failed. Select the address and copy it manually.")) } })
    },
  })
  const save = useMutation({
    mutationFn: (v: Values) => {
      const expiresAt = v.expires ? new Date(`${v.expires}T23:59:59.999`).toISOString() : null
      return editing === "new"
        ? api<Alias>(`/organizations/${orgId}/personal-aliases`, { method: "POST", body: { ...target, localPart: v.localPart, note: v.note, expiresAt } })
        : api<Alias>(`/aliases/${(editing as Alias).id}`, { method: "PATCH", body: { note: v.note, expiresAt: form.getFieldState("expires").isDirty ? expiresAt : undefined } })
    },
    meta: { silent: true },
    onSuccess: (a) => {
      refresh()
      setEditing(null)
      toast.success(editing === "new" ? `Created ${a.address}` : `Saved ${a.address}`)
    },
    onError: (error) => {
      const field = error instanceof ApiError ? fieldErrors[error.code] : undefined
      if (field) form.setError(field, { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const toggle = useMutation({
    mutationFn: (a: Alias) => api<Alias>(`/aliases/${a.id}`, { method: "PATCH", body: { active: !a.active } }),
    onSuccess: (a) => {
      refresh()
      toast.success(a.active ? `${a.address} receives mail again` : `${a.address} is off. Mail sent to it is rejected.`)
    },
  })
  const remove = useMutation({
    mutationFn: (a: Alias) => api(`/aliases/${a.id}`, { method: "DELETE" }),
    onSuccess: (_d, a) => {
      refresh()
      setDeleting(null)
      toast.success(`Deleted ${a.address}`)
    },
  })
  const key = useMutation({
    mutationFn: (revoke: boolean) => api<{ token: string } | undefined>(`/organizations/${orgId}/personal-alias-key`, revoke ? { method: "DELETE" } : { method: "PUT", body: target }),
    onSuccess: (data, revoke) => {
      setToken(data?.token ?? "")
      setConfirmKey(null)
      refresh()
      if (revoke) toast.success("Password manager key revoked")
    },
  })

  function edit(alias: Alias | "new") {
    const existing = alias === "new" ? null : alias
    form.reset({ localPart: "", note: existing?.note ?? "", expires: existing?.expiresAt ? dateValue(new Date(existing.expiresAt)) : "" })
    setEditing(alias)
  }

  if (policy.isPending || list.isPending) return null
  const failed = policy.isError ? policy : list.isError ? list : null
  if (!failed && !open && !all.length && !keyMailboxId) return null

  const usage = !open
    ? "Your organization has stopped new personal aliases. The ones below keep working."
    : full
      ? `You’ve used all ${limit}. Delete one to create another.`
      : `${all.length} of ${limit} used.`

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Personal aliases"
        description={`Give each site or sign-up its own address. Mail to it arrives in ${mailbox.address}, and when one starts getting spam you can turn it off, which rejects mail sent to it.`}
        actions={
          <>
            {picker}
            {open && !failed && (
              <>
                <Button variant="secondary" disabled={full} onClick={() => edit("new")}>
                  <Plus aria-hidden />
                  Create alias
                </Button>
                <Button variant="secondary" disabled={full} loading={generate.isPending} onClick={() => generate.mutate()}>
                  <Shuffle aria-hidden />
                  Generate masked address
                </Button>
              </>
            )}
          </>
        }
      />
      {failed ? (
        <ErrorState error={failed.error} onRetry={() => failed.refetch()} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-fg-secondary">
            <span>{usage}</span>
            {open && domains.length > 1 && (
              <span className="flex flex-wrap items-center gap-2">
                <span className="whitespace-nowrap">New addresses end in</span>
                <Select value={domain.id} onValueChange={setDomainChoice}>
                  <SelectTrigger aria-label="Domain for new addresses" size="sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {domains.map((d) => (
                      <SelectItem key={d.id} value={d.id}>
                        @{d.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </span>
            )}
            {open && domains.length === 1 && (
              <span>
                New addresses end in <span className="font-mono">@{domain.name}</span>.
              </span>
            )}
          </div>
          {aliases.length ? (
            <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
              {aliases.map((a) => (
                <li key={a.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 p-4">
                  <Switch
                    aria-label={`${a.active ? "Turn off" : "Turn on"} ${a.address}`}
                    checked={a.active}
                    disabled={toggle.isPending && toggle.variables?.id === a.id}
                    onCheckedChange={() => toggle.mutate(a)}
                  />
                  <div className="flex min-w-0 flex-1 basis-56 flex-col gap-1">
                    <div className="flex items-center gap-2">
                      <span className="min-w-0 font-mono text-sm break-all">{a.address}</span>
                      <CopyButton value={a.address} label={`Copy ${a.address}`} />
                    </div>
                    {(a.note || a.expiresAt) && (
                      <div className="flex flex-wrap items-center gap-2 text-sm text-fg-tertiary">
                        {expired(a) && <StatusBadge tone="neutral">Expired</StatusBadge>}
                        <span className="min-w-0 break-words">
                          {a.note}
                          {a.note && a.expiresAt && !expired(a) && " · "}
                          {a.expiresAt && !expired(a) && `Expires ${formatDate(a.expiresAt)}`}
                        </span>
                      </div>
                    )}
                  </div>
                  <div className="ml-auto flex gap-1">
                    <IconButton label={`Edit ${a.address}`} size="icon-sm" onClick={() => edit(a)}>
                      <Pencil />
                    </IconButton>
                    <IconButton label={`Delete ${a.address}`} size="icon-sm" onClick={() => setDeleting(a)}>
                      <Trash2 />
                    </IconButton>
                  </div>
                </li>
              ))}
            </ul>
          ) : (
            <EmptyState
              icon={VenetianMask}
              title="No personal aliases yet"
              description={`Generate a masked address the next time a site asks for your email. If it leaks, you’ll know who leaked it, and you can switch it off without touching ${mailbox.address}.`}
              className="rounded-xl border border-border"
            />
          )}
          {(open || !!keyMailboxId) && (
            <PasswordManagerKey
              token={token}
              active={!!keyMailboxId}
              target={open ? `@${domain.name}` : undefined}
              mailbox={mailbox.address}
              pending={key.isPending}
              onCreate={open ? () => (keyMailboxId ? setConfirmKey("replace") : key.mutate(false)) : undefined}
              onRevoke={() => setConfirmKey("revoke")}
            />
          )}
        </>
      )}

      <FormDialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
        title={editing === "new" ? "Create alias" : "Edit alias"}
        description={editing === "new" ? `Choose the address yourself. Mail to it arrives in ${mailbox.address}.` : <span className="font-mono">{editing?.address}</span>}
        submitLabel={editing === "new" ? "Create alias" : "Save alias"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => {
          if (editing === "new" && !v.localPart) form.setError("localPart", { message: "Enter the part of the address before the @." })
          else save.mutate(v)
        })}
      >
        {editing === "new" && (
          <Field id="personal-local" label="Address" error={errors.localPart?.message}>
            <div className="flex items-center gap-2">
              <Input id="personal-local" placeholder="supplier-portal" maxLength={64} autoComplete="off" spellCheck={false} aria-invalid={!!errors.localPart} {...form.register("localPart")} />
              <span className="font-mono text-sm whitespace-nowrap text-fg-secondary">@{domain?.name}</span>
            </div>
          </Field>
        )}
        <Field id="personal-note" label="Note" hint="Where you used this address, so you can tell where mail to it comes from." error={errors.note?.message}>
          <Input id="personal-note" placeholder="Conference registration" maxLength={1000} autoComplete="off" aria-invalid={!!errors.note} {...form.register("note")} />
        </Field>
        <Field id="personal-expires" label="Expires" hint="Optional. Mail sent to it is rejected after this day." error={errors.expires?.message}>
          <Input id="personal-expires" type="date" min={dateValue(new Date())} aria-invalid={!!errors.expires} className="w-48" {...form.register("expires")} />
        </Field>
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.address ?? "alias"}?`}
        description="Mail sent to it is rejected from now on, and someone else in your organization could then take the address. To stop mail but keep the address, turn it off instead."
        confirmLabel="Delete alias"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
      <ConfirmDialog
        open={!!confirmKey}
        onOpenChange={(o) => !o && setConfirmKey(null)}
        title={confirmKey === "replace" ? "Replace your password manager key?" : "Revoke your password manager key?"}
        description={
          confirmKey === "replace"
            ? "The current key stops working at once. Paste the new key into your password manager after you copy it."
            : "Your password manager can no longer create addresses. The addresses it already created keep working."
        }
        confirmLabel={confirmKey === "replace" ? "Replace key" : "Revoke key"}
        pending={key.isPending}
        onConfirm={() => key.mutate(confirmKey === "revoke")}
      />
    </section>
  )
}

function PasswordManagerKey({
  token,
  active,
  target,
  mailbox,
  pending,
  onCreate,
  onRevoke,
}: {
  token: string
  active: boolean
  target?: string
  mailbox: string
  pending: boolean
  onCreate?: () => void
  onRevoke: () => void
}) {
  const server = window.location.origin

  return (
    <div className="flex flex-col gap-4 border-t border-border pt-6">
      <div className="flex flex-col gap-1">
        <span className="font-semibold">Password manager</span>
        <p className="text-sm text-fg-secondary">
          Bitwarden can create a masked address each time it fills in a sign-up form. In its generator, choose Forwarded email alias, then SimpleLogin, and enter the server address and key below. Any app that works with a self-hosted SimpleLogin server can use them too.
        </p>
      </div>
      <dl className="flex flex-col rounded-xl border border-border">
        <div className="flex flex-col gap-1 border-b border-border p-4 sm:flex-row sm:items-center sm:gap-4">
          <dt className="w-32 shrink-0 text-label text-fg-tertiary">Server address</dt>
          <dd className="flex min-w-0 items-center gap-2">
            <span className="font-mono text-sm break-all">{server}</span>
            <CopyButton value={server} label="Copy server address" />
          </dd>
        </div>
        <div className="flex flex-col gap-1 p-4 sm:flex-row sm:items-center sm:gap-4">
          <dt className="w-32 shrink-0 text-label text-fg-tertiary">Key</dt>
          <dd className="flex min-w-0 items-center gap-2 text-sm">
            {token ? (
              <>
                <span className="font-mono break-all">{token}</span>
                <CopyButton value={token} label="Copy key" />
              </>
            ) : active ? (
              <StatusBadge tone="success">Active</StatusBadge>
            ) : (
              <span className="text-fg-secondary">None yet</span>
            )}
          </dd>
        </div>
      </dl>
      {token && <p className="text-sm text-fg-tertiary">Copy the key now. Aether shows it only once.</p>}
      {target && !token && <p className="text-sm text-fg-tertiary">{`A new key creates addresses ending in ${target} that arrive in ${mailbox}.`}</p>}
      <div className="flex flex-wrap gap-3">
        {onCreate && (
          <Button variant="secondary" loading={pending && !active} onClick={onCreate}>
            {active ? "Replace key" : "Create key"}
          </Button>
        )}
        {active && (
          <Button variant="quiet" onClick={onRevoke}>
            Revoke key
          </Button>
        )}
      </div>
    </div>
  )
}

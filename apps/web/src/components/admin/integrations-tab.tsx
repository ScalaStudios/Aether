"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Ban, History, KeyRound, Plus, SearchX, Send, Trash2, Webhook as WebhookIcon } from "lucide-react"
import { useMemo, useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyField } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SearchInput } from "@/components/search-input"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { useSession } from "@/hooks/use-session"
import { ApiError, api } from "@/lib/api"
import { formatDate, formatDateTime } from "@/lib/format"
import { useI18n } from "@/lib/i18n"
import { hasPerm } from "@/lib/permissions"
import type { APIKey, Suppression, Webhook, WebhookDelivery } from "@/lib/types"
import { featuresQuery } from "@/lib/queries"

type Access = "" | "read" | "write"

const resources: { value: string; levels: Access[] }[] = [
  { value: "domains", levels: ["", "read", "write"] },
  { value: "mailboxes", levels: ["", "read", "write"] },
  { value: "aliases", levels: ["", "read", "write"] },
  { value: "members", levels: ["", "read", "write"] },
  { value: "audit", levels: ["", "read"] },
  { value: "reports", levels: ["", "read", "write"] },
  { value: "webhooks", levels: ["", "read", "write"] },
  { value: "contacts", levels: ["", "read", "write"] },
  { value: "message-trace", levels: ["", "read"] },
  { value: "senders", levels: ["", "read", "write"] },
  { value: "mailflow", levels: ["", "read", "write"] },
  { value: "suppressions", levels: ["", "read", "write"] },
]

interface KeyValues {
  name: string
  access: Record<string, Access>
  expires: string
}

const keyDefaults: KeyValues = { name: "", access: Object.fromEntries(resources.map((r) => [r.value, "" as Access])), expires: "" }

const splitEvents = (events: string) =>
  events
    .split(",")
    .map((e) => e.trim())
    .filter(Boolean)

interface HookValues {
  url: string
  all: boolean
  events: string
}

const deliveryTones: Record<WebhookDelivery["status"], "success" | "info" | "danger"> = {
  delivered: "success",
  pending: "info",
  failed: "danger",
}

const chip = "rounded-xs bg-muted px-2 py-1 font-mono text-xs"

export function IntegrationsTab({ orgId }: { orgId: string }) {
  const session = useSession().data
  const canReadKeys = hasPerm(session, orgId, "api_keys:read")
  const canWriteKeys = hasPerm(session, orgId, "api_keys:write")
  const canReadHooks = hasPerm(session, orgId, "webhooks:read")
  const canWriteHooks = hasPerm(session, orgId, "webhooks:write")

  const state = useQuery(featuresQuery(orgId))

  return (
    <QueryView query={state} skeleton={<RowsSkeleton rows={4} />}>
      {({ features }) => (
        <div className="flex flex-col gap-12">
          {canReadKeys && features.apiKeys && <APIKeys orgId={orgId} canWrite={canWriteKeys} webhooks={features.webhooks} />}
          {canReadHooks && features.webhooks && <Webhooks orgId={orgId} canWrite={canWriteHooks} />}
          {canReadHooks && <Suppressions orgId={orgId} />}
        </div>
      )}
    </QueryView>
  )
}

function APIKeys({ orgId, canWrite, webhooks }: { orgId: string; canWrite: boolean; webhooks: boolean }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const keys = useQuery({ queryKey: ["api-keys", orgId], queryFn: () => api<APIKey[]>(`/organizations/${orgId}/api-keys`) })
  const [creating, setCreating] = useState(false)
  const [token, setToken] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<APIKey | null>(null)
  const keySchema = useMemo(
    () =>
      z.object({
        name: z.string().trim().min(1, t("admin.integrations.apiKeys.validation.nameRequired")).max(100, t("admin.integrations.apiKeys.validation.nameMax")),
        access: z.record(z.string(), z.enum(["", "read", "write"])).refine((a) => Object.values(a).some(Boolean), t("admin.integrations.apiKeys.validation.scopeRequired")),
        expires: z.string(),
      }),
    [t]
  )
  const levelLabels: Record<Access, string> = {
    "": t("admin.integrations.apiKeys.levels.none"),
    read: t("admin.integrations.apiKeys.levels.read"),
    write: t("admin.integrations.apiKeys.levels.write"),
  }
  const form = useForm<KeyValues>({ resolver: zodResolver(keySchema), defaultValues: keyDefaults })

  const create = useMutation({
    mutationFn: (v: KeyValues) =>
      api<{ key: APIKey; token: string }>(`/organizations/${orgId}/api-keys`, {
        method: "POST",
        body: {
          name: v.name,
          scopes: Object.entries(v.access).flatMap(([resource, access]) => (access ? [`${resource}:${access}`] : [])),
          expiresAt: v.expires ? new Date(`${v.expires}T23:59:59`).toISOString() : undefined,
        },
      }),
    onSuccess: (data) => {
      setToken(data.token)
      form.reset()
      qc.invalidateQueries({ queryKey: ["api-keys", orgId] })
    },
  })
  const revoke = useMutation({
    mutationFn: (id: string) => api(`/api-keys/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(t("admin.integrations.apiKeys.revokedToast"))
      setRevoking(null)
      qc.invalidateQueries({ queryKey: ["api-keys", orgId] })
    },
  })

  function close(open: boolean) {
    setCreating(open)
    if (!open) {
      setToken(null)
      form.reset()
    }
  }

  const today = new Date().toLocaleDateString("en-CA")

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title={t("admin.integrations.apiKeys.title")}
        description={t("admin.integrations.apiKeys.description")}
        actions={
          canWrite && (
            <Button onClick={() => setCreating(true)}>
              <Plus aria-hidden />
              {t("admin.integrations.apiKeys.create")}
            </Button>
          )
        }
      />
      <QueryView query={keys} empty={<EmptyState icon={KeyRound} title={t("admin.integrations.apiKeys.emptyTitle")} description={t("admin.integrations.apiKeys.emptyDescription")} />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("common.name")}</TableHead>
                <TableHead>{t("admin.integrations.apiKeys.key")}</TableHead>
                <TableHead>{t("admin.integrations.apiKeys.scopes")}</TableHead>
                <TableHead>{t("admin.integrations.apiKeys.lastUsed")}</TableHead>
                <TableHead>{t("admin.integrations.apiKeys.expires")}</TableHead>
                {canWrite && (
                  <TableHead className="w-12">
                    <span className="sr-only">{t("common.actions")}</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell className="font-mono text-sm">{k.prefix}…</TableCell>
                  <TableCell className="whitespace-normal">
                    <div className="flex flex-wrap gap-1">
                      {k.scopes.map((s) => (
                        <span key={s} className={chip}>
                          {s}
                        </span>
                      ))}
                    </div>
                  </TableCell>
                  <TableCell>{k.lastUsedAt ? formatDateTime(k.lastUsedAt) : t("common.never")}</TableCell>
                  <TableCell>{k.expiresAt ? formatDate(k.expiresAt) : t("common.never")}</TableCell>
                  {canWrite && (
                    <TableCell>
                      <IconButton label={t("admin.integrations.apiKeys.revokeAria", { name: k.name })} size="icon-sm" onClick={() => setRevoking(k)}>
                        <Trash2 />
                      </IconButton>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={creating}
        onOpenChange={close}
        title={token ? t("admin.integrations.apiKeys.createdTitle") : t("admin.integrations.apiKeys.create")}
        description={token ? t("admin.integrations.apiKeys.createdDescription") : t("admin.integrations.apiKeys.createDescription")}
        submitLabel={t("admin.integrations.apiKeys.create")}
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
        footer={
          token && (
            <DialogClose asChild>
              <Button type="button">{t("common.done")}</Button>
            </DialogClose>
          )
        }
      >
        {token ? (
          <CopyField id="api-key-token" label={t("admin.integrations.apiKeys.token")} value={token} copyLabel={t("admin.integrations.apiKeys.copyToken")} />
        ) : (
          <>
            <Field id="api-key-name" label={t("common.name")} error={form.formState.errors.name?.message}>
              <Input id="api-key-name" placeholder={t("admin.integrations.apiKeys.namePlaceholder")} autoComplete="off" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
            </Field>
            <Controller
              control={form.control}
              name="access"
              render={({ field, fieldState }) => (
                <fieldset className="flex flex-col gap-4">
                  <legend className="mb-2 text-label text-fg-secondary">{t("admin.integrations.apiKeys.scopes")}</legend>
                  {resources.filter((r) => webhooks || r.value !== "webhooks").map((r) => {
                    const label = t(`admin.integrations.apiKeys.resources.${r.value}`)
                    return (
                      <div key={r.value} className="flex items-center justify-between gap-4">
                        <div className="flex flex-col gap-1">
                          <span>{label}</span>
                          <span className="text-sm text-fg-tertiary">{t(`admin.integrations.apiKeys.resources.${r.value}Description`)}</span>
                        </div>
                        <SegmentedControl
                          label={t("admin.integrations.apiKeys.accessLabel", { resource: label })}
                          options={r.levels.map((level) => ({ value: level, label: levelLabels[level] }))}
                          value={field.value[r.value]}
                          onChange={(level) => field.onChange({ ...field.value, [r.value]: level })}
                        />
                      </div>
                    )
                  })}
                  {fieldState.error && (
                    <p role="alert" className="text-sm text-danger">
                      {fieldState.error.message}
                    </p>
                  )}
                </fieldset>
              )}
            />
            <Field id="api-key-expires" label={t("admin.integrations.apiKeys.expires")} hint={t("admin.integrations.apiKeys.expiresHint")}>
              <Input id="api-key-expires" type="date" min={today} {...form.register("expires")} />
            </Field>
          </>
        )}
      </FormDialog>

      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(open) => !open && setRevoking(null)}
        title={t("admin.integrations.apiKeys.revokeTitle")}
        description={t("admin.integrations.apiKeys.revokeDescription", { name: revoking?.name ?? "" })}
        confirmLabel={t("admin.integrations.apiKeys.revokeConfirm")}
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </section>
  )
}

export function Webhooks({ orgId, canWrite = true }: { orgId?: string; canWrite?: boolean }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const collection = orgId ? `/organizations/${orgId}/webhooks` : "/admin/webhooks"
  const item = orgId ? "/webhooks" : "/admin/webhooks"
  const key = ["webhooks", orgId ?? "platform"]
  const hooks = useQuery({ queryKey: key, queryFn: () => api<Webhook[]>(collection) })
  const [creating, setCreating] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<Webhook | null>(null)
  const [viewing, setViewing] = useState<Webhook | null>(null)
  const hookSchema = useMemo(
    () =>
      z
        .object({
          url: z.url(t("admin.integrations.webhooks.validation.urlInvalid")),
          all: z.boolean(),
          events: z.string(),
        })
        .refine((v) => v.all || splitEvents(v.events).length > 0, { message: t("admin.integrations.webhooks.validation.eventsRequired"), path: ["events"] }),
    [t]
  )
  const form = useForm<HookValues>({ resolver: zodResolver(hookSchema), defaultValues: { url: "", all: true, events: "" } })
  const all = useWatch({ control: form.control, name: "all" })

  const create = useMutation({
    mutationFn: (v: HookValues) =>
      api<{ webhook: Webhook; secret: string }>(collection, {
        method: "POST",
        body: { url: v.url, events: v.all ? ["*"] : splitEvents(v.events) },
      }),
    onSuccess: (data) => {
      setSecret(data.secret)
      form.reset()
      qc.invalidateQueries({ queryKey: key })
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) => api(`${item}/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success(t("admin.integrations.webhooks.deletedToast"))
      setDeleting(null)
      qc.invalidateQueries({ queryKey: key })
    },
  })
  const test = useMutation({
    mutationFn: (hook: Webhook) => api(`${item}/${hook.id}/test`, { method: "POST" }),
    onSuccess: (_d, hook) => {
      toast.success(t("admin.integrations.webhooks.testQueuedToast"))
      qc.invalidateQueries({ queryKey: ["webhook-deliveries", hook.id] })
      setViewing(hook)
    },
  })

  function close(open: boolean) {
    setCreating(open)
    if (!open) {
      setSecret(null)
      form.reset()
    }
  }

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title={orgId ? t("admin.integrations.webhooks.title") : "Platform webhooks"}
        description={
          orgId
            ? "Aether posts matching audit and mail delivery events to these URLs as JSON. Verify the Aether-Signature header with your secret."
            : "Aether posts audit events that belong to no organization, such as identity provider changes and sign-ins of accounts without a mailbox, to these URLs as JSON. Verify the Aether-Signature header with your secret."
        }
        actions={
          canWrite && (
            <Button variant={orgId ? "secondary" : "default"} onClick={() => setCreating(true)}>
              <Plus aria-hidden />
              {t("admin.integrations.webhooks.create")}
            </Button>
          )
        }
      />
      <QueryView query={hooks} empty={<EmptyState icon={WebhookIcon} title="No webhooks" description={`Create a webhook to receive events such as ${orgId ? "mailbox.create or message.bounced" : "auth.login.failed or scim.user.create"}.`} />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("admin.integrations.webhooks.url")}</TableHead>
                <TableHead>{t("admin.integrations.webhooks.events")}</TableHead>
                <TableHead>{t("admin.integrations.webhooks.created")}</TableHead>
                <TableHead className="w-28">
                  <span className="sr-only">{t("common.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((h) => (
                <TableRow key={h.id}>
                  <TableCell className="min-w-56 font-mono text-sm break-all whitespace-normal">{h.url}</TableCell>
                  <TableCell className="whitespace-normal">
                    {h.events.includes("*") ? (
                      t("admin.integrations.webhooks.allEvents")
                    ) : (
                      <div className="flex flex-wrap gap-1">
                        {h.events.map((e) => (
                          <span key={e} className={chip}>
                            {e}
                          </span>
                        ))}
                      </div>
                    )}
                  </TableCell>
                  <TableCell>{formatDate(h.createdAt)}</TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      {canWrite && (
                        <IconButton label={t("admin.integrations.webhooks.sendTestAria")} size="icon-sm" disabled={test.isPending} onClick={() => test.mutate(h)}>
                          <Send />
                        </IconButton>
                      )}
                      <IconButton label={t("admin.integrations.webhooks.deliveriesAria")} size="icon-sm" onClick={() => setViewing(h)}>
                        <History />
                      </IconButton>
                      {canWrite && (
                        <IconButton label={t("admin.integrations.webhooks.deleteAria", { url: h.url })} size="icon-sm" onClick={() => setDeleting(h)}>
                          <Trash2 />
                        </IconButton>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={creating}
        onOpenChange={close}
        title={secret ? t("admin.integrations.webhooks.createdTitle") : t("admin.integrations.webhooks.create")}
        description={secret ? t("admin.integrations.webhooks.createdDescription") : t("admin.integrations.webhooks.createDescription")}
        submitLabel={t("admin.integrations.webhooks.create")}
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
        footer={
          secret && (
            <DialogClose asChild>
              <Button type="button">{t("common.done")}</Button>
            </DialogClose>
          )
        }
      >
        {secret ? (
          <CopyField id="webhook-secret" label={t("admin.integrations.webhooks.signingSecret")} value={secret} copyLabel={t("admin.integrations.webhooks.copySecret")} />
        ) : (
          <>
            <Field id="webhook-url" label={t("admin.integrations.webhooks.url")} hint={t("admin.integrations.webhooks.urlHint")} error={form.formState.errors.url?.message}>
              <Input id="webhook-url" type="url" placeholder="https://example.com/aether" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!form.formState.errors.url} {...form.register("url")} />
            </Field>
            <SwitchField control={form.control} name="all" id="webhook-all" label={t("admin.integrations.webhooks.allEvents")} />
            {!all && (
              <Field id="webhook-events" label="Events" hint={`Comma-separated event names, for example ${orgId ? "mailbox.create, message.bounced, message.delivered" : "auth.login.failed, scim.user.create"}.`} error={form.formState.errors.events?.message}>
                <Input id="webhook-events" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!form.formState.errors.events} {...form.register("events")} />
              </Field>
            )}
          </>
        )}
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("admin.integrations.webhooks.deleteTitle")}
        description={t("admin.integrations.webhooks.deleteDescription", { url: deleting?.url ?? "" })}
        confirmLabel={t("admin.integrations.webhooks.deleteConfirm")}
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />

      {viewing && <DeliveriesDialog key={viewing.id} webhook={viewing} path={`${item}/${viewing.id}/deliveries`} onClose={() => setViewing(null)} />}
    </section>
  )
}

function DeliveriesDialog({ webhook, path, onClose }: { webhook: Webhook; path: string; onClose: () => void }) {
  const { t } = useI18n()
  const deliveries = useQuery({
    queryKey: ["webhook-deliveries", webhook.id],
    queryFn: () => api<WebhookDelivery[]>(path),
    refetchInterval: (q) => (q.state.data?.some((d) => d.status === "pending") ? 5000 : false),
  })

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="grid-cols-1 sm:max-w-[800px]">
        <DialogHeader>
          <DialogTitle>{t("admin.integrations.webhooks.deliveriesTitle")}</DialogTitle>
          <DialogDescription className="break-all">{t("admin.integrations.webhooks.deliveriesDescription", { url: webhook.url })}</DialogDescription>
        </DialogHeader>
        <QueryView query={deliveries} empty={<EmptyState icon={History} title={t("admin.integrations.webhooks.noDeliveriesTitle")} description={t("admin.integrations.webhooks.noDeliveriesDescription")} />}>
          {(items) => (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("admin.integrations.webhooks.time")}</TableHead>
                  <TableHead>{t("admin.integrations.webhooks.event")}</TableHead>
                  <TableHead>{t("common.status")}</TableHead>
                  <TableHead>{t("admin.integrations.webhooks.response")}</TableHead>
                  <TableHead>{t("admin.integrations.webhooks.error")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((d) => (
                  <TableRow key={d.id}>
                    <TableCell>{formatDateTime(d.deliveredAt ?? d.createdAt)}</TableCell>
                    <TableCell className="font-mono text-sm">{d.event}</TableCell>
                    <TableCell>
                      <StatusBadge tone={deliveryTones[d.status]}>{t(`admin.integrations.webhooks.deliveryStatus.${d.status}`)}</StatusBadge>
                    </TableCell>
                    <TableCell className="font-mono text-sm">{d.responseCode ?? "—"}</TableCell>
                    <TableCell className="text-sm break-words whitespace-normal text-fg-secondary">{d.lastError || "—"}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </QueryView>
      </DialogContent>
    </Dialog>
  )
}

const suppressionSchema = z.object({
  address: z.string().trim().pipe(z.email("Enter an email address such as someone@example.com.").max(320, "Use up to 320 characters.")),
  reason: z.enum(["manual", "complaint"]),
  detail: z.string().trim().max(500, "Use up to 500 characters."),
})

type SuppressionValues = z.infer<typeof suppressionSchema>

const suppressionReasons: Record<Suppression["reason"], { tone: "danger" | "warning" | "neutral"; label: string }> = {
  bounce: { tone: "danger", label: "Hard bounce" },
  complaint: { tone: "warning", label: "Complaint" },
  manual: { tone: "neutral", label: "Added manually" },
}

function Suppressions({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 250)
  const list = useQuery({
    queryKey: ["suppressions", orgId, q],
    queryFn: () => api<Suppression[]>(`/organizations/${orgId}/suppressions`, { query: { q } }),
    placeholderData: keepPreviousData,
  })
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<Suppression | null>(null)
  const form = useForm<SuppressionValues>({ resolver: zodResolver(suppressionSchema), defaultValues: { address: "", reason: "manual", detail: "" } })

  const add = useMutation({
    mutationFn: (v: SuppressionValues) => api<Suppression>(`/organizations/${orgId}/suppressions`, { method: "POST", body: v }),
    meta: { silent: true },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "already_suppressed") {
        form.setError("address", { message: error.message }, { shouldFocus: true })
      } else {
        toast.error(error.message)
      }
    },
    onSuccess: (s) => {
      toast.success(`${s.address} added to the suppression list`)
      setAdding(false)
      form.reset()
      qc.invalidateQueries({ queryKey: ["suppressions", orgId] })
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) => api(`/suppressions/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Address removed. Aether sends to it again.")
      setRemoving(null)
      qc.invalidateQueries({ queryKey: ["suppressions", orgId] })
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Suppression list"
        description="Aether refuses to send to these addresses from this organization’s domains. Addresses that hard-bounce are added automatically."
        actions={
          <Button variant="secondary" onClick={() => setAdding(true)}>
            <Plus aria-hidden />
            Add address
          </Button>
        }
      />
      <SearchInput aria-label="Search the suppression list" placeholder="Search by address" value={search} onChange={(e) => setSearch(e.target.value)} className="max-w-md" />
      <QueryView
        query={list}
        empty={
          q ? (
            <EmptyState icon={SearchX} title="No matches" description={`No suppressed addresses match “${q}”.`} />
          ) : (
            <EmptyState icon={Ban} title="No suppressed addresses" description="Addresses that hard-bounce appear here, so your applications stop mailing them." />
          )
        }
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Reason</TableHead>
                <TableHead>Details</TableHead>
                <TableHead>Added</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((s) => (
                <TableRow key={s.id}>
                  <TableCell className="font-mono text-sm break-all whitespace-normal">{s.address}</TableCell>
                  <TableCell>
                    <StatusBadge tone={suppressionReasons[s.reason].tone}>{suppressionReasons[s.reason].label}</StatusBadge>
                  </TableCell>
                  <TableCell className="text-sm break-words whitespace-normal text-fg-secondary">{s.detail || "—"}</TableCell>
                  <TableCell>{formatDateTime(s.createdAt)}</TableCell>
                  <TableCell>
                    <IconButton label={`Remove ${s.address}`} size="icon-sm" onClick={() => setRemoving(s)}>
                      <Trash2 />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>
      {list.data?.length === 200 && <p className="text-sm text-fg-tertiary">Showing the 200 most recent addresses. Search to find others.</p>}

      <FormDialog
        open={adding}
        onOpenChange={(open) => {
          setAdding(open)
          if (!open) form.reset()
        }}
        title="Add to suppression list"
        description="Aether rejects messages to this address until you remove it."
        submitLabel="Add address"
        pending={add.isPending}
        onSubmit={form.handleSubmit((v) => add.mutate(v))}
      >
        <Field id="suppression-address" label="Address" error={form.formState.errors.address?.message}>
          <Input id="suppression-address" type="email" placeholder="someone@example.com" autoComplete="off" spellCheck={false} aria-invalid={!!form.formState.errors.address} aria-describedby={form.formState.errors.address ? "suppression-address-error" : undefined} {...form.register("address")} />
        </Field>
        <Controller
          control={form.control}
          name="reason"
          render={({ field }) => (
            <fieldset className="flex flex-col">
              <legend className="mb-2 text-label text-fg-secondary">Reason</legend>
              <SegmentedControl
                label="Reason"
                options={[
                  { value: "manual", label: "Manual" },
                  { value: "complaint", label: "Complaint" },
                ]}
                value={field.value}
                onChange={field.onChange}
              />
            </fieldset>
          )}
        />
        <Field id="suppression-detail" label="Note" hint="Optional." error={form.formState.errors.detail?.message}>
          <Input id="suppression-detail" autoComplete="off" aria-invalid={!!form.formState.errors.detail} aria-describedby={form.formState.errors.detail ? "suppression-detail-error" : undefined} {...form.register("detail")} />
        </Field>
      </FormDialog>

      <ConfirmDialog
        open={!!removing}
        onOpenChange={(open) => !open && setRemoving(null)}
        title="Remove from suppression list?"
        description={`Aether sends to ${removing?.address} again. If the address still bounces, it is added back.`}
        confirmLabel="Remove"
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </section>
  )
}

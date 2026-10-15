"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { History, KeyRound, Plus, Send, Trash2, Webhook as WebhookIcon } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"
import { cn } from "cn"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyField } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import { formatDate, formatDateTime } from "@/lib/format"
import type { APIKey, Webhook, WebhookDelivery } from "@/lib/types"

type Access = "" | "read" | "write"

const resources: { value: string; label: string; levels: Access[] }[] = [
  { value: "domains", label: "Domains", levels: ["", "read", "write"] },
  { value: "mailboxes", label: "Mailboxes", levels: ["", "read", "write"] },
  { value: "aliases", label: "Aliases", levels: ["", "read", "write"] },
  { value: "members", label: "Members", levels: ["", "read", "write"] },
  { value: "audit", label: "Audit log", levels: ["", "read"] },
  { value: "webhooks", label: "Webhooks", levels: ["", "read", "write"] },
]

const levelLabels: Record<Access, string> = { "": "None", read: "Read", write: "Write" }

const keySchema = z.object({
  name: z.string().trim().min(1, "Enter a name.").max(100, "Use up to 100 characters."),
  access: z.record(z.string(), z.enum(["", "read", "write"])).refine((a) => Object.values(a).some(Boolean), "Choose at least one scope."),
  expires: z.string(),
})

type KeyValues = z.infer<typeof keySchema>

const keyDefaults: KeyValues = { name: "", access: Object.fromEntries(resources.map((r) => [r.value, ""])), expires: "" }

const splitEvents = (events: string) =>
  events
    .split(",")
    .map((e) => e.trim())
    .filter(Boolean)

const hookSchema = z
  .object({
    url: z.url("Enter a full URL such as https://example.com/aether."),
    all: z.boolean(),
    events: z.string(),
  })
  .refine((v) => v.all || splitEvents(v.events).length > 0, { message: "Enter at least one event name.", path: ["events"] })

type HookValues = z.infer<typeof hookSchema>

const deliveryStatus: Record<WebhookDelivery["status"], { tone: "success" | "info" | "danger"; label: string }> = {
  delivered: { tone: "success", label: "Delivered" },
  pending: { tone: "info", label: "Pending" },
  failed: { tone: "danger", label: "Failed" },
}

const chip = "rounded-xs bg-muted px-2 py-1 font-mono text-xs"

export function IntegrationsTab({ orgId }: { orgId: string }) {
  return (
    <div className="flex flex-col gap-12">
      <APIKeys orgId={orgId} />
      <Webhooks orgId={orgId} />
    </div>
  )
}

function APIKeys({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const keys = useQuery({ queryKey: ["api-keys", orgId], queryFn: () => api<APIKey[]>(`/organizations/${orgId}/api-keys`) })
  const [creating, setCreating] = useState(false)
  const [token, setToken] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<APIKey | null>(null)
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
      toast.success("API key revoked")
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
        title="API keys"
        description="A key acts as an administrator of this organization, limited to its scopes. Send it as a bearer token."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            Create API key
          </Button>
        }
      />
      <QueryView query={keys} empty={<EmptyState icon={KeyRound} title="No API keys" description="Create a key to manage domains, mailboxes and aliases from scripts." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>Scopes</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead>Expires</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Actions</span>
                </TableHead>
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
                  <TableCell>{k.lastUsedAt ? formatDateTime(k.lastUsedAt) : "Never"}</TableCell>
                  <TableCell>{k.expiresAt ? formatDate(k.expiresAt) : "Never"}</TableCell>
                  <TableCell>
                    <IconButton label={`Revoke ${k.name}`} size="icon-sm" onClick={() => setRevoking(k)}>
                      <Trash2 />
                    </IconButton>
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
        title={token ? "API key created" : "Create API key"}
        description={token ? "Copy this token now. You won't be able to see it again." : "A write scope includes read access."}
        submitLabel="Create API key"
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
        footer={
          token && (
            <DialogClose asChild>
              <Button type="button">Done</Button>
            </DialogClose>
          )
        }
      >
        {token ? (
          <CopyField id="api-key-token" label="Token" value={token} copyLabel="Copy token" />
        ) : (
          <>
            <Field id="api-key-name" label="Name" error={form.formState.errors.name?.message}>
              <Input id="api-key-name" placeholder="Provisioning script" autoComplete="off" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
            </Field>
            <Controller
              control={form.control}
              name="access"
              render={({ field, fieldState }) => (
                <fieldset className="flex flex-col gap-2">
                  <legend className="mb-2 text-label text-fg-secondary">Scopes</legend>
                  {resources.map((r) => (
                    <div key={r.value} className="flex items-center justify-between gap-4">
                      <span>{r.label}</span>
                      <div role="group" aria-label={`${r.label} access`} className="flex gap-1 rounded-lg border border-strong p-1">
                        {r.levels.map((level) => (
                          <Button
                            key={level}
                            type="button"
                            variant="quiet"
                            size="sm"
                            aria-pressed={field.value[r.value] === level}
                            className={cn("max-md:min-h-11", field.value[r.value] === level && "bg-muted text-foreground")}
                            onClick={() => field.onChange({ ...field.value, [r.value]: level })}
                          >
                            {levelLabels[level]}
                          </Button>
                        ))}
                      </div>
                    </div>
                  ))}
                  {fieldState.error && (
                    <p role="alert" className="text-sm text-danger">
                      {fieldState.error.message}
                    </p>
                  )}
                </fieldset>
              )}
            />
            <Field id="api-key-expires" label="Expires" hint="Optional. The key stops working at the end of this day.">
              <Input id="api-key-expires" type="date" min={today} {...form.register("expires")} />
            </Field>
          </>
        )}
      </FormDialog>

      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(open) => !open && setRevoking(null)}
        title="Revoke API key?"
        description={`Requests using “${revoking?.name}” are rejected immediately. This cannot be undone.`}
        confirmLabel="Revoke"
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </section>
  )
}

function Webhooks({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const hooks = useQuery({ queryKey: ["webhooks", orgId], queryFn: () => api<Webhook[]>(`/organizations/${orgId}/webhooks`) })
  const [creating, setCreating] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<Webhook | null>(null)
  const [viewing, setViewing] = useState<Webhook | null>(null)
  const form = useForm<HookValues>({ resolver: zodResolver(hookSchema), defaultValues: { url: "", all: true, events: "" } })
  const all = useWatch({ control: form.control, name: "all" })

  const create = useMutation({
    mutationFn: (v: HookValues) =>
      api<{ webhook: Webhook; secret: string }>(`/organizations/${orgId}/webhooks`, {
        method: "POST",
        body: { url: v.url, events: v.all ? ["*"] : splitEvents(v.events) },
      }),
    onSuccess: (data) => {
      setSecret(data.secret)
      form.reset()
      qc.invalidateQueries({ queryKey: ["webhooks", orgId] })
    },
  })
  const remove = useMutation({
    mutationFn: (id: string) => api(`/webhooks/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Webhook deleted")
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ["webhooks", orgId] })
    },
  })
  const test = useMutation({
    mutationFn: (hook: Webhook) => api(`/webhooks/${hook.id}/test`, { method: "POST" }),
    onSuccess: (_d, hook) => {
      toast.success("Test event queued. Aether sends it within 15 seconds.")
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
        title="Webhooks"
        description="Aether posts matching audit events to these URLs as JSON. Verify the Aether-Signature header with your secret."
        actions={
          <Button variant="secondary" onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            Create webhook
          </Button>
        }
      />
      <QueryView query={hooks} empty={<EmptyState icon={WebhookIcon} title="No webhooks" description="Create a webhook to receive events such as mailbox.create or domain.verify." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>URL</TableHead>
                <TableHead>Events</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="w-28">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((h) => (
                <TableRow key={h.id}>
                  <TableCell className="font-mono text-sm break-all whitespace-normal">{h.url}</TableCell>
                  <TableCell className="whitespace-normal">
                    {h.events.includes("*") ? (
                      "All events"
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
                      <IconButton label="Send test event" size="icon-sm" disabled={test.isPending} onClick={() => test.mutate(h)}>
                        <Send />
                      </IconButton>
                      <IconButton label="Deliveries" size="icon-sm" onClick={() => setViewing(h)}>
                        <History />
                      </IconButton>
                      <IconButton label={`Delete webhook to ${h.url}`} size="icon-sm" onClick={() => setDeleting(h)}>
                        <Trash2 />
                      </IconButton>
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
        title={secret ? "Webhook created" : "Create webhook"}
        description={secret ? "Copy this secret now. You won't be able to see it again." : "Aether signs each request with a secret shown after you create the webhook."}
        submitLabel="Create webhook"
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
        footer={
          secret && (
            <DialogClose asChild>
              <Button type="button">Done</Button>
            </DialogClose>
          )
        }
      >
        {secret ? (
          <CopyField id="webhook-secret" label="Signing secret" value={secret} copyLabel="Copy secret" />
        ) : (
          <>
            <Field id="webhook-url" label="URL" hint="Must use https." error={form.formState.errors.url?.message}>
              <Input id="webhook-url" type="url" placeholder="https://example.com/aether" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!form.formState.errors.url} {...form.register("url")} />
            </Field>
            <SwitchField control={form.control} name="all" id="webhook-all" label="All events" />
            {!all && (
              <Field id="webhook-events" label="Events" hint="Comma-separated audit actions, for example mailbox.create, domain.verify." error={form.formState.errors.events?.message}>
                <Input id="webhook-events" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!form.formState.errors.events} {...form.register("events")} />
              </Field>
            )}
          </>
        )}
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title="Delete webhook?"
        description={`Aether stops sending events to ${deleting?.url} and deletes its delivery history.`}
        confirmLabel="Delete webhook"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />

      {viewing && <DeliveriesDialog key={viewing.id} webhook={viewing} onClose={() => setViewing(null)} />}
    </section>
  )
}

function DeliveriesDialog({ webhook, onClose }: { webhook: Webhook; onClose: () => void }) {
  const deliveries = useQuery({
    queryKey: ["webhook-deliveries", webhook.id],
    queryFn: () => api<WebhookDelivery[]>(`/webhooks/${webhook.id}/deliveries`),
    refetchInterval: (q) => (q.state.data?.some((d) => d.status === "pending") ? 5000 : false),
  })

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-[800px]">
        <DialogHeader>
          <DialogTitle>Deliveries</DialogTitle>
          <DialogDescription className="break-all">The 50 most recent deliveries to {webhook.url}, newest first.</DialogDescription>
        </DialogHeader>
        <QueryView query={deliveries} empty={<EmptyState icon={History} title="No deliveries yet" description="Send a test event to check that your endpoint responds." />}>
          {(items) => (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Event</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Response</TableHead>
                  <TableHead>Error</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((d) => (
                  <TableRow key={d.id}>
                    <TableCell>{formatDateTime(d.deliveredAt ?? d.createdAt)}</TableCell>
                    <TableCell className="font-mono text-sm">{d.event}</TableCell>
                    <TableCell>
                      <StatusBadge tone={deliveryStatus[d.status].tone}>{deliveryStatus[d.status].label}</StatusBadge>
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

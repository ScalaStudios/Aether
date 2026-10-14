"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { KeyRound, Pencil, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useFieldArray, useForm, useWatch, type UseFormReturn } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyButton } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api } from "@/lib/api"
import type { Discovery, IdentityProvider, IdentityProviderInput, Organization, Role } from "@/lib/types"

const HALO_SCOPES = "openid profile email groups"

const makeSchema = (requireSecret: boolean) =>
  z.object({
    issuer: z.url("Enter the issuer URL, for example https://halo.example.com."),
    name: z.string().trim().min(1, "Enter the name shown on the sign-in button."),
    slug: z.string().regex(/^[a-z0-9][a-z0-9-]{0,62}$/, "Use lowercase letters, digits and hyphens."),
    clientId: z.string().trim().min(1, "Enter the client ID."),
    clientSecret: requireSecret ? z.string().min(1, "Enter the client secret.") : z.string(),
    scopes: z.string().trim().min(1, "Enter at least the openid scope."),
    autoProvision: z.boolean(),
    adminGroup: z.string().trim(),
    groupMappings: z.array(
      z.object({
        group: z.string().trim().min(1, "Enter a group name."),
        orgId: z.string().min(1, "Choose an organization."),
        role: z.enum(["owner", "admin", "member"]),
      })
    ),
    enabled: z.boolean(),
  })

type Values = z.infer<ReturnType<typeof makeSchema>>

type Editing = { mode: "halo" } | { mode: "generic" } | { mode: "edit"; provider: IdentityProvider }

export function IdentityProvidersTab({ organizations }: { organizations: Organization[] }) {
  const qc = useQueryClient()
  const providers = useQuery({ queryKey: ["identity-providers"], queryFn: () => api<IdentityProvider[]>("/admin/identity-providers") })
  const [editing, setEditing] = useState<Editing | null>(null)
  const [deleting, setDeleting] = useState<IdentityProvider | null>(null)

  const remove = useMutation({
    mutationFn: (id: string) => api(`/admin/identity-providers/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Identity provider deleted")
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ["identity-providers"] })
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Identity providers"
        description="Let people sign in with an OpenID Connect provider. Halo is the recommended provider for Scala Studios installations."
        actions={
          <>
            <Button variant="secondary" onClick={() => setEditing({ mode: "generic" })}>
              <Plus aria-hidden />
              Add OIDC provider
            </Button>
            <Button onClick={() => setEditing({ mode: "halo" })}>
              <KeyRound aria-hidden />
              Connect Halo
            </Button>
          </>
        }
      />
      <QueryView query={providers} empty={<EmptyState icon={KeyRound} title="No identity providers" description="Connect Halo or another OpenID Connect provider to enable single sign-on." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Slug</TableHead>
                <TableHead>Issuer</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="w-32">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((p) => (
                <TableRow key={p.id}>
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <span className="font-medium">{p.name}</span>
                      <span className="rounded-xs bg-muted px-2 py-1 text-xs">{p.kind === "halo" ? "Halo" : "OIDC"}</span>
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-sm">{p.slug}</TableCell>
                  <TableCell className="font-mono text-sm">{p.issuer}</TableCell>
                  <TableCell>
                    <StatusBadge tone={p.enabled ? "success" : "neutral"}>{p.enabled ? "Enabled" : "Disabled"}</StatusBadge>
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <CopyButton value={p.redirectUri} label="Copy redirect URI" />
                      <IconButton label={`Edit ${p.name}`} size="icon-sm" onClick={() => setEditing({ mode: "edit", provider: p })}>
                        <Pencil />
                      </IconButton>
                      <IconButton label={`Delete ${p.name}`} size="icon-sm" onClick={() => setDeleting(p)}>
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

      {editing && (
        <ProviderDialog
          key={editing.mode === "edit" ? editing.provider.id : editing.mode}
          editing={editing}
          organizations={organizations}
          onClose={() => setEditing(null)}
        />
      )}

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Delete ${deleting?.name}?`}
        description="People who sign in through this provider can no longer sign in with it. Their accounts and mailboxes are kept."
        confirmLabel="Delete provider"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </section>
  )
}

function defaults(editing: Editing): Values {
  if (editing.mode === "edit") {
    const p = editing.provider
    return {
      issuer: p.issuer,
      name: p.name,
      slug: p.slug,
      clientId: p.clientId,
      clientSecret: "",
      scopes: p.scopes.join(" "),
      autoProvision: p.autoProvision,
      adminGroup: p.adminGroup,
      groupMappings: p.groupMappings,
      enabled: p.enabled,
    }
  }
  const halo = editing.mode === "halo"
  return {
    issuer: "",
    name: halo ? "Halo" : "",
    slug: halo ? "halo" : "",
    clientId: "",
    clientSecret: "",
    scopes: halo ? HALO_SCOPES : "openid profile email",
    autoProvision: false,
    adminGroup: "",
    groupMappings: [],
    enabled: true,
  }
}

const steps = ["Issuer", "Client credentials", "Provisioning"]

function ProviderDialog({ editing, organizations, onClose }: { editing: Editing; organizations: Organization[]; onClose: () => void }) {
  const qc = useQueryClient()
  const wizard = editing.mode === "halo"
  const form = useForm<Values>({ resolver: zodResolver(makeSchema(editing.mode !== "edit")), defaultValues: defaults(editing) })
  const [step, setStep] = useState(0)
  const [discovery, setDiscovery] = useState<Discovery | null>(null)
  const issuer = useWatch({ control: form.control, name: "issuer" })
  const slug = useWatch({ control: form.control, name: "slug" })

  const discover = useMutation({
    mutationFn: (value: string) => api<Discovery>("/admin/identity-providers/discover", { method: "POST", body: { issuer: value } }),
    meta: { silent: true },
    onSuccess: setDiscovery,
  })

  const save = useMutation({
    mutationFn: (v: Values) => {
      const body: IdentityProviderInput = {
        ...v,
        issuer: wizard && discovery ? discovery.issuer : v.issuer,
        kind: editing.mode === "edit" ? editing.provider.kind : wizard ? "halo" : "generic",
        clientSecret: v.clientSecret || undefined,
        scopes: v.scopes.split(/\s+/).filter(Boolean),
      }
      return editing.mode === "edit"
        ? api<IdentityProvider>(`/admin/identity-providers/${editing.provider.id}`, { method: "PUT", body })
        : api<IdentityProvider>("/admin/identity-providers", { method: "POST", body })
    },
    onSuccess: (p) => {
      toast.success(editing.mode === "edit" ? `Saved ${p.name}` : `${p.name} is connected`)
      qc.invalidateQueries({ queryKey: ["identity-providers"] })
      onClose()
    },
  })

  const discovered = discovery !== null && discover.variables === issuer
  const redirectUri = discovered
    ? discovery.redirectUri.replace("{slug}", slug || "{slug}")
    : editing.mode === "edit"
      ? editing.provider.redirectUri
      : undefined

  async function next() {
    if (step === 0) {
      if (!(await form.trigger("issuer"))) return
      if (!discovered) {
        discover.mutate(issuer)
        return
      }
      setStep(1)
    } else if (step === 1) {
      if (await form.trigger(["name", "slug", "clientId", "clientSecret"])) setStep(2)
    }
  }

  const submit = form.handleSubmit((v) => save.mutate(v))
  const errors = form.formState.errors

  const credentials = (
    <>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field id="idp-name" label="Name" hint="Shown on the sign-in button." error={errors.name?.message}>
          <Input id="idp-name" autoComplete="off" aria-invalid={!!errors.name} {...form.register("name")} />
        </Field>
        <Field id="idp-slug" label="Slug" hint="Part of the redirect URI." error={errors.slug?.message}>
          <Input id="idp-slug" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!errors.slug} {...form.register("slug")} />
        </Field>
      </div>
      <Field id="idp-client-id" label="Client ID" error={errors.clientId?.message}>
        <Input id="idp-client-id" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!errors.clientId} {...form.register("clientId")} />
      </Field>
      <Field
        id="idp-client-secret"
        label="Client secret"
        hint={editing.mode === "edit" ? (editing.provider.hasClientSecret ? "Leave empty to keep the stored secret." : "No secret is stored.") : undefined}
        error={errors.clientSecret?.message}
      >
        <Input id="idp-client-secret" type="password" autoComplete="off" aria-invalid={!!errors.clientSecret} {...form.register("clientSecret")} />
      </Field>
      {redirectUri && <RedirectUri value={redirectUri} />}
    </>
  )

  const issuerField = (
    <Field id="idp-issuer" label="Issuer URL" hint={wizard ? "The base URL of your Halo installation." : undefined} error={errors.issuer?.message}>
      <Input id="idp-issuer" type="url" placeholder="https://halo.example.com" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!errors.issuer} {...form.register("issuer")} />
    </Field>
  )

  if (!wizard) {
    return (
      <FormDialog
        open
        onOpenChange={(open) => !open && onClose()}
        wide
        title={editing.mode === "edit" ? `Edit ${editing.provider.name}` : "Add OIDC provider"}
        description="Any OpenID Connect provider that supports the authorization code flow."
        submitLabel={editing.mode === "edit" ? "Save changes" : "Add provider"}
        pending={save.isPending}
        onSubmit={submit}
      >
        {issuerField}
        {credentials}
        <Field id="idp-scopes" label="Scopes" hint="Separated by spaces." error={errors.scopes?.message}>
          <Input id="idp-scopes" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!errors.scopes} {...form.register("scopes")} />
        </Field>
        <Provisioning form={form} organizations={organizations} />
        <SwitchField form={form} name="enabled" label="Enabled" hint="Show this provider on the sign-in page." />
      </FormDialog>
    )
  }

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      wide
      title="Connect Halo"
      description={`Step ${step + 1} of 3 · ${steps[step]}`}
      submitLabel="Save"
      pending={save.isPending}
      onSubmit={(e) => {
        if (step < 2) {
          e.preventDefault()
          next()
        } else submit(e)
      }}
      footer={
        <>
          {step > 0 && (
            <Button type="button" variant="secondary" onClick={() => setStep(step - 1)}>
              Back
            </Button>
          )}
          <Button type="submit" loading={discover.isPending || save.isPending}>
            {step === 0 && !discovered ? "Check issuer" : step < 2 ? "Continue" : "Connect Halo"}
          </Button>
        </>
      }
    >
      {step === 0 && (
        <>
          {issuerField}
          {discover.isError && (
            <p role="alert" className="text-sm text-danger">
              {discover.error.message}
            </p>
          )}
          {discovered && <DiscoveryResult discovery={discovery} redirectUri={redirectUri ?? ""} />}
        </>
      )}
      {step === 1 && credentials}
      {step === 2 && <Provisioning form={form} organizations={organizations} />}
    </FormDialog>
  )
}

function RedirectUri({ value }: { value: string }) {
  return (
    <div className="flex flex-col gap-2">
      <span className="text-label text-fg-secondary">Redirect URI</span>
      <div className="flex items-center gap-2 rounded-lg border border-border bg-sunken py-1 pr-1 pl-4">
        <code className="min-w-0 flex-1 font-mono text-sm break-all">{value}</code>
        <CopyButton value={value} label="Copy redirect URI" />
      </div>
    </div>
  )
}

function DiscoveryResult({ discovery, redirectUri }: { discovery: Discovery; redirectUri: string }) {
  const endpoints: [string, string | undefined][] = [
    ["Authorization", discovery.authorizationEndpoint],
    ["Token", discovery.tokenEndpoint],
    ["User info", discovery.userinfoEndpoint],
    ["End session", discovery.endSessionEndpoint],
  ]

  return (
    <div className="animate-content flex flex-col gap-4">
      <div className="flex flex-wrap gap-2">
        <StatusBadge tone={discovery.pkceS256 ? "success" : "danger"}>{discovery.pkceS256 ? "PKCE S256 supported" : "PKCE S256 not supported"}</StatusBadge>
        <StatusBadge tone={discovery.groupsClaim ? "success" : "warning"}>{discovery.groupsClaim ? "Groups claim supported" : "Groups claim not advertised"}</StatusBadge>
      </div>
      <dl className="flex flex-col rounded-xl border border-border">
        {endpoints
          .filter(([, value]) => value)
          .map(([label, value]) => (
            <div key={label} className="flex flex-col gap-1 border-b border-border p-3 last:border-b-0 sm:flex-row sm:gap-4">
              <dt className="w-28 shrink-0 text-label text-fg-tertiary">{label}</dt>
              <dd className="font-mono text-sm break-all">{value}</dd>
            </div>
          ))}
      </dl>
      <RedirectUri value={redirectUri} />
      <div className="flex flex-col gap-2 rounded-xl border border-border bg-sunken p-4 text-sm text-fg-secondary">
        <p className="font-semibold text-foreground">Register Aether in Halo</p>
        <ol className="flex list-decimal flex-col gap-1 pl-6">
          <li>Create an application with the type Web.</li>
          <li>Add the redirect URI above.</li>
          <li>
            Allow the scopes <code className="font-mono">{HALO_SCOPES}</code>.
          </li>
          <li>Copy the client ID and secret Halo shows, then continue.</li>
        </ol>
      </div>
    </div>
  )
}

function SwitchField({ form, name, label, hint }: { form: UseFormReturn<Values>; name: "enabled" | "autoProvision"; label: string; hint: string }) {
  const id = `idp-${name}`
  return (
    <Controller
      control={form.control}
      name={name}
      render={({ field }) => (
        <div className="flex items-start gap-3">
          <Switch id={id} checked={field.value} onCheckedChange={field.onChange} className="mt-1" />
          <div className="flex flex-col gap-1">
            <Label htmlFor={id}>{label}</Label>
            <p className="text-sm text-fg-tertiary">{hint}</p>
          </div>
        </div>
      )}
    />
  )
}

function Provisioning({ form, organizations }: { form: UseFormReturn<Values>; organizations: Organization[] }) {
  const mappings = useFieldArray({ control: form.control, name: "groupMappings" })
  const errors = form.formState.errors.groupMappings

  return (
    <div className="flex flex-col gap-4">
      <SwitchField form={form} name="autoProvision" label="Create accounts on first sign-in" hint="When off, only people who already have an Aether account or mailbox can sign in." />
      <Field id="idp-admin-group" label="Platform administrator group" hint="Members of this group become platform administrators. Leave empty to grant nobody.">
        <Input id="idp-admin-group" autoComplete="off" spellCheck={false} className="font-mono" {...form.register("adminGroup")} />
      </Field>
      <div className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <span className="text-label text-fg-secondary">Group mappings</span>
          <p className="text-sm text-fg-tertiary">Members of a group join the organization with the chosen role when they sign in.</p>
        </div>
        {mappings.fields.map((row, index) => (
          <div key={row.id} className="grid gap-2 border-b border-border pb-3 sm:grid-cols-[1fr_1fr_160px_auto] sm:border-b-0 sm:pb-0">
            <div className="flex flex-col gap-1">
              <Input
                aria-label={`Group ${index + 1}`}
                placeholder="Group name"
                autoComplete="off"
                className="font-mono"
                aria-invalid={!!errors?.[index]?.group}
                {...form.register(`groupMappings.${index}.group`)}
              />
              {errors?.[index]?.group && <p className="text-sm text-danger">{errors[index]?.group?.message}</p>}
            </div>
            <Controller
              control={form.control}
              name={`groupMappings.${index}.orgId`}
              render={({ field, fieldState }) => (
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger aria-label={`Organization for mapping ${index + 1}`} aria-invalid={!!fieldState.error} className="w-full">
                    <SelectValue placeholder="Organization" />
                  </SelectTrigger>
                  <SelectContent>
                    {organizations.map((o) => (
                      <SelectItem key={o.id} value={o.id}>
                        {o.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
            <Controller
              control={form.control}
              name={`groupMappings.${index}.role`}
              render={({ field }) => (
                <Select value={field.value} onValueChange={(v) => field.onChange(v as Role)}>
                  <SelectTrigger aria-label={`Role for mapping ${index + 1}`} className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="member">Member</SelectItem>
                    <SelectItem value="admin">Administrator</SelectItem>
                    <SelectItem value="owner">Owner</SelectItem>
                  </SelectContent>
                </Select>
              )}
            />
            <IconButton type="button" label={`Remove mapping ${index + 1}`} onClick={() => mappings.remove(index)} className="justify-self-end">
              <Trash2 />
            </IconButton>
          </div>
        ))}
        <Button
          type="button"
          variant="secondary"
          size="sm"
          className="self-start"
          disabled={organizations.length === 0}
          onClick={() => mappings.append({ group: "", orgId: organizations[0]?.id ?? "", role: "member" })}
        >
          <Plus aria-hidden />
          Add mapping
        </Button>
      </div>
    </div>
  )
}

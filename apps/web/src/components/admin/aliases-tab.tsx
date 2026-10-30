"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Forward, Pencil, Plus, Trash2, Users } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { DomainSelect } from "@/components/admin/domain-select"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, ApiError, errorMessage } from "@/lib/api"
import { plural } from "@/lib/format"
import { domainsQuery, membersQuery } from "@/lib/queries"
import type { AcceptFrom, Alias, Member } from "@/lib/types"

type Kind = Alias["kind"]

const addresses = z.array(z.object({ name: z.string().optional(), address: z.string() }))

const schema = z
  .object({
    kind: z.enum(["alias", "list"]),
    domainId: z.string().min(1, "Choose a domain."),
    localPart: z
      .string()
      .trim()
      .toLowerCase()
      .regex(/^([a-z0-9]([a-z0-9._+-]*[a-z0-9])?)?$/, "Use letters, digits, dots, hyphens, plus signs or underscores."),
    name: z.string().trim().max(200, "Use 200 characters or fewer."),
    destinations: addresses.min(1, "Add at least one address."),
    active: z.boolean(),
    acceptFrom: z.enum(["anyone", "organization", "members", "allowlist"]),
    allowedSenders: addresses,
    sendAs: addresses,
    owners: addresses,
  })
  .refine((v) => v.kind === "list" || v.destinations.length <= 100, { message: "An alias forwards to up to 100 addresses. Make it a distribution list for more.", path: ["destinations"] })
  .refine((v) => v.destinations.length <= 5000, { message: "A list can have up to 5,000 members.", path: ["destinations"] })
  .refine((v) => v.kind === "alias" || v.localPart !== "", { message: "Enter the list’s address. Only an alias can be a catch-all.", path: ["localPart"] })
  .refine((v) => v.kind === "alias" || v.acceptFrom !== "allowlist" || v.allowedSenders.length > 0, { message: "Add at least one address or @domain.", path: ["allowedSenders"] })

type Values = z.infer<typeof schema>

const blank: Values = { kind: "alias", domainId: "", localPart: "", name: "", destinations: [], active: true, acceptFrom: "organization", allowedSenders: [], sendAs: [], owners: [] }

const kinds = [
  { value: "alias" as const, label: "Alias" },
  { value: "list" as const, label: "Distribution list" },
]

const kindHints: Record<Kind, string> = {
  alias: "Forwards to up to 100 addresses. Every mailbox among them can send as the alias.",
  list: "Up to 5,000 members. You choose who can send to the list and who can send as it.",
}

const senders: Record<AcceptFrom, { label: string; hint: string }> = {
  anyone: { label: "Anyone", hint: "Including people outside this organization." },
  organization: { label: "This organization", hint: "Only people signed in to send from this organization’s domains. Mail from anyone else is rejected." },
  members: { label: "List members", hint: "Only the list’s members. Mail from anyone else is rejected." },
  allowlist: { label: "Specific senders", hint: "Only the addresses and domains below. Mail from anyone else is rejected." },
}

const fieldErrors: Record<string, keyof Values> = {
  invalid_local_part: "localPart",
  address_taken: "localPart",
  invalid_domain: "domainId",
  invalid_name: "name",
  invalid_destinations: "destinations",
  invalid_send_as: "sendAs",
  invalid_owners: "owners",
}

const aliasesQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["aliases", orgId],
    queryFn: () => api<Alias[]>(`/organizations/${orgId}/aliases`),
  })

const toAddresses = (list: string[]) => list.map((address) => ({ address }))

function toValues(target: Alias | Kind, people: Member[]): Values {
  if (typeof target === "string") return { ...blank, kind: target }
  return {
    kind: target.kind,
    domainId: target.domainId,
    localPart: target.localPart,
    name: target.name,
    destinations: toAddresses(target.destinations),
    active: target.active,
    acceptFrom: target.acceptFrom,
    allowedSenders: toAddresses(target.allowedSenders),
    sendAs: toAddresses(target.sendAs),
    owners: target.owners.map((id) => {
      const person = people.find((p) => p.userId === id)
      return person ? { name: person.displayName || undefined, address: person.email } : { address: id }
    }),
  }
}

export function AliasesTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const aliases = useQuery({ ...aliasesQuery(orgId), select: (all) => all.filter((a) => a.kind === "alias") })
  const lists = useQuery({ ...aliasesQuery(orgId), select: (all) => all.filter((a) => a.kind === "list") })
  const domains = useQuery(domainsQuery(orgId))
  const [editing, setEditing] = useState<Alias | Kind | null>(null)
  const [deleting, setDeleting] = useState<Alias | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: blank })
  const [kind, acceptFrom] = useWatch({ control: form.control, name: ["kind", "acceptFrom"] })
  const errors = form.formState.errors
  const existing = typeof editing === "object" ? editing : null

  const domainName = (id: string) => domains.data?.find((d) => d.id === id)?.name ?? ""
  const label = (a: Alias) => (a.localPart ? a.address : `Any address @${domainName(a.domainId)}`)

  async function open(target: Alias | Kind) {
    try {
      form.reset(toValues(target, await qc.ensureQueryData(membersQuery(orgId))))
      setEditing(target)
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }

  const save = useMutation({
    mutationFn: async (v: Values) => {
      const list = v.kind === "list"
      const people = list ? await qc.ensureQueryData(membersQuery(orgId)) : []
      const owners = list ? v.owners.map((o) => ({ ...o, id: people.find((p) => p.email.toLowerCase() === o.address.toLowerCase())?.userId })) : []
      const outside = owners.filter((o) => !o.id).map((o) => o.address)
      if (outside.length) {
        throw new ApiError(422, "invalid_owners", `${outside.join(", ")} ${outside.length === 1 ? "isn’t a member" : "aren’t members"} of this organization. Add them under Members first.`)
      }
      const body = {
        kind: v.kind,
        name: list ? v.name : undefined,
        destinations: v.destinations.map((a) => a.address),
        acceptFrom: list ? v.acceptFrom : "anyone",
        allowedSenders: list && v.acceptFrom === "allowlist" ? v.allowedSenders.map((a) => a.address) : [],
        sendAs: list ? v.sendAs.map((a) => a.address) : [],
        owners: owners.map((o) => o.id),
      }
      return existing
        ? api<Alias>(`/aliases/${existing.id}`, { method: "PATCH", body: { ...body, active: v.active } })
        : api<Alias>(`/organizations/${orgId}/aliases`, { method: "POST", body: { ...body, domainId: v.domainId, localPart: v.localPart } })
    },
    meta: { silent: true },
    onSuccess: (saved) => {
      toast.success(existing ? `Saved ${label(saved)}` : `Created ${label(saved)}`)
      qc.invalidateQueries({ queryKey: ["aliases", orgId] })
      setEditing(null)
    },
    onError: (error) => {
      const field = error instanceof ApiError ? fieldErrors[error.code] : undefined
      if (field) form.setError(field, { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const remove = useMutation({
    mutationFn: (a: Alias) => api(`/aliases/${a.id}`, { method: "DELETE" }),
    onSuccess: (_d, a) => {
      toast.success(`Deleted ${label(a)}`)
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ["aliases", orgId] })
    },
  })

  const actions = (a: Alias) => (
    <div className="flex gap-1">
      <IconButton label={`Edit ${label(a)}`} size="icon-sm" onClick={() => open(a)}>
        <Pencil />
      </IconButton>
      <IconButton label={`Delete ${label(a)}`} size="icon-sm" onClick={() => setDeleting(a)}>
        <Trash2 />
      </IconButton>
    </div>
  )

  return (
    <div className="flex flex-col gap-12">
      <section className="flex flex-col gap-6">
        <SectionHeader
          title="Aliases"
          description="Forward mail for an address to up to 100 destinations. Every mailbox among them can send as the alias. A catch-all receives mail for addresses that have no mailbox or alias."
          actions={
            <Button onClick={() => open("alias")}>
              <Plus aria-hidden />
              Create alias
            </Button>
          }
        />
        <QueryView query={aliases} empty={<EmptyState icon={Forward} title="No aliases" description="Create an alias to forward an address without a separate mailbox." />}>
          {(items) => (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Address</TableHead>
                  <TableHead>Destinations</TableHead>
                  <TableHead className="w-20">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((a) => (
                  <TableRow key={a.id}>
                    <TableCell>
                      <div className="flex items-center gap-3">
                        <span className="font-mono text-sm">{label(a)}</span>
                        {!a.localPart && <StatusBadge tone="info">Catch-all</StatusBadge>}
                        {!a.active && <StatusBadge tone="neutral">Inactive</StatusBadge>}
                      </div>
                    </TableCell>
                    <TableCell className="font-mono text-sm whitespace-normal break-all">{a.destinations.join(", ")}</TableCell>
                    <TableCell>{actions(a)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </QueryView>
      </section>

      <section className="flex flex-col gap-6">
        <SectionHeader
          title="Distribution lists"
          description="One address for a group of up to 5,000 people, listed in the directory. Choose who can send to it, who can send as it, and who can change its members without being an administrator."
          actions={
            <Button variant="secondary" onClick={() => open("list")}>
              <Plus aria-hidden />
              Create list
            </Button>
          }
        />
        <QueryView query={lists} empty={<EmptyState icon={Users} title="No distribution lists" description="Create a list such as staff@ to reach a group, and decide who is allowed to write to it." />}>
          {(items) => (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>List</TableHead>
                  <TableHead>Members</TableHead>
                  <TableHead>Who can send</TableHead>
                  <TableHead className="w-20">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((a) => (
                  <TableRow key={a.id}>
                    <TableCell>
                      <div className="flex items-center gap-3">
                        <div className="flex min-w-0 flex-col">
                          <span className="truncate font-medium">{a.name || a.address}</span>
                          {a.name && <span className="truncate font-mono text-sm text-fg-tertiary">{a.address}</span>}
                        </div>
                        {!a.active && <StatusBadge tone="neutral">Inactive</StatusBadge>}
                      </div>
                    </TableCell>
                    <TableCell>{plural(a.destinations.length, "member")}</TableCell>
                    <TableCell>{senders[a.acceptFrom].label}</TableCell>
                    <TableCell>{actions(a)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </QueryView>
      </section>

      <FormDialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
        title={`${existing ? "Edit" : "Create"} ${kind === "list" ? "distribution list" : "alias"}`}
        description={existing ? <span className="font-mono">{label(existing)}</span> : kindHints[kind]}
        submitLabel={`${existing ? "Save" : "Create"} ${kind === "list" ? "list" : "alias"}`}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
      >
        {existing?.localPart && (
          <Controller
            control={form.control}
            name="kind"
            render={({ field }) => (
              <fieldset className="flex flex-col items-start gap-2">
                <legend className="mb-2 text-label text-fg-secondary">Type</legend>
                <SegmentedControl label="Type" options={kinds} value={field.value} onChange={field.onChange} />
                <p className="text-sm text-fg-tertiary">{kindHints[field.value]}</p>
              </fieldset>
            )}
          />
        )}
        {!existing && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="alias-local" label="Local part" hint={kind === "alias" ? "Leave empty for a catch-all." : undefined} error={errors.localPart?.message}>
              <Input id="alias-local" placeholder={kind === "list" ? "staff" : "sales"} autoComplete="off" spellCheck={false} aria-invalid={!!errors.localPart} {...form.register("localPart")} />
            </Field>
            <DomainSelect id="alias-domain" orgId={orgId} control={form.control} />
          </div>
        )}
        {kind === "list" && (
          <Field id="alias-name" label="Name" hint="Optional. Shown in the directory and in address suggestions." error={errors.name?.message}>
            <Input id="alias-name" placeholder="Staff" autoComplete="off" aria-invalid={!!errors.name} {...form.register("name")} />
          </Field>
        )}
        <Controller
          control={form.control}
          name="destinations"
          render={({ field, fieldState }) => (
            <Field
              id="alias-destinations"
              label={kind === "list" ? "Members" : "Destinations"}
              hint={kind === "list" ? "Mailboxes, other lists or outside addresses. Press Enter or type a comma after each one." : "Press Enter or type a comma after each address."}
              error={fieldState.error?.message}
            >
              <RecipientInput id="alias-destinations" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
            </Field>
          )}
        />
        {existing && <SwitchField control={form.control} name="active" id="alias-active" label="Active" hint="Turn it off to stop forwarding mail without deleting it." />}
        {kind === "list" && (
          <>
            <Controller
              control={form.control}
              name="acceptFrom"
              render={({ field }) => (
                <Field id="alias-accept" label="Who can send to the list" hint={senders[field.value].hint}>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="alias-accept" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(Object.keys(senders) as AcceptFrom[]).map((s) => (
                        <SelectItem key={s} value={s}>
                          {senders[s].label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
            {acceptFrom === "allowlist" && (
              <Controller
                control={form.control}
                name="allowedSenders"
                render={({ field, fieldState }) => (
                  <Field id="alias-allowed" label="Allowed senders" hint="Addresses, or @example.com for everyone at a domain." error={fieldState.error?.message}>
                    <RecipientInput id="alias-allowed" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="@example.com" allowDomains />
                  </Field>
                )}
              />
            )}
            <Controller
              control={form.control}
              name="sendAs"
              render={({ field, fieldState }) => (
                <Field id="alias-send-as" label="Can send as the list" hint="Mailboxes in this organization that can send from the list’s address. Members can’t unless you add them here." error={fieldState.error?.message}>
                  <RecipientInput id="alias-send-as" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
                </Field>
              )}
            />
            <Controller
              control={form.control}
              name="owners"
              render={({ field, fieldState }) => (
                <Field id="alias-owners" label="Owners" hint="People in this organization who can add and remove members from their settings, without being administrators." error={fieldState.error?.message}>
                  <RecipientInput id="alias-owners" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
                </Field>
              )}
            />
          </>
        )}
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={deleting?.kind === "list" ? "Delete distribution list?" : "Delete alias?"}
        description={deleting ? `Mail to ${label(deleting)} stops being forwarded immediately.` : ""}
        confirmLabel={deleting?.kind === "list" ? "Delete list" : "Delete alias"}
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </div>
  )
}

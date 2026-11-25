"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Download, Forward, ListTree, Pencil, Plus, Trash2, Upload, Users, VenetianMask } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { CsvImportDialog } from "@/components/admin/csv-import-dialog"
import { DomainSelect } from "@/components/admin/domain-select"
import { PersonalAliasPolicyHeader } from "@/components/admin/personal-alias-policy"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { MultiSelect } from "@/components/multi-select"
import { SectionHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { SegmentedControl } from "@/components/segmented-control"
import { expired } from "@/components/settings/personal-aliases"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api, ApiError, errorMessage } from "@/lib/api"
import { downloadCsv, exportAliasesCsv, exportListsCsv } from "@/lib/csv"
import { plural } from "@/lib/format"
import { hasPerm } from "@/lib/permissions"
import { domainsQuery, mailboxesQuery, membersQuery } from "@/lib/queries"
import type { AcceptFrom, Alias, DirectoryGroup, Member, ResolvedMember } from "@/lib/types"

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
      .regex(/^([a-z0-9]([a-z0-9._-]*[a-z0-9])?)?$/, "Use letters, digits, dots, hyphens or underscores. A plus sign marks a tag, so it can’t be part of an address."),
    name: z.string().trim().max(200, "Use 200 characters or fewer."),
    destinations: addresses,
    active: z.boolean(),
    acceptFrom: z.enum(["anyone", "organization", "members", "allowlist"]),
    allowedSenders: addresses,
    sendAs: addresses,
    sentMailboxId: z.string(),
    owners: addresses,
    groups: z.array(z.string()),
    memberDomains: z.array(z.string()),
    moderated: z.boolean(),
    moderators: addresses,
    moderationExempt: addresses,
    hidden: z.boolean(),
  })
  .refine((v) => v.kind === "list" || v.destinations.length > 0, { message: "Add at least one address.", path: ["destinations"] })
  .refine((v) => v.kind === "alias" || v.destinations.length + v.groups.length + v.memberDomains.length > 0, { message: "Add at least one member, group or domain.", path: ["destinations"] })
  .refine((v) => v.kind === "list" || v.destinations.length <= 100, { message: "An alias forwards to up to 100 addresses. Make it a distribution list for more.", path: ["destinations"] })
  .refine((v) => v.destinations.length <= 5000, { message: "A list can have up to 5,000 members.", path: ["destinations"] })
  .refine((v) => v.kind === "alias" || v.localPart !== "", { message: "Enter the list’s address. Only an alias can be a catch-all.", path: ["localPart"] })
  .refine((v) => v.kind === "alias" || v.acceptFrom !== "allowlist" || v.allowedSenders.length > 0, { message: "Add at least one address or @domain.", path: ["allowedSenders"] })
  .refine((v) => v.kind === "alias" || !v.moderated || v.moderators.length > 0 || v.owners.length > 0, { message: "Add a moderator, or an owner to approve messages.", path: ["moderators"] })

type Values = z.infer<typeof schema>

const blank: Values = { kind: "alias", domainId: "", localPart: "", name: "", destinations: [], active: true, acceptFrom: "organization", allowedSenders: [], sendAs: [], sentMailboxId: "", owners: [], groups: [], memberDomains: [], moderated: false, moderators: [], moderationExempt: [], hidden: false }

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
  invalid_sent_mailbox: "sentMailboxId",
  invalid_send_as: "sendAs",
  invalid_owners: "owners",
  invalid_moderators: "moderators",
  invalid_groups: "groups",
  invalid_member_domains: "memberDomains",
}

const aliasesQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["aliases", orgId],
    queryFn: () => api<Alias[]>(`/organizations/${orgId}/aliases`),
  })

const directoryGroupsQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["directory-groups", orgId],
    queryFn: () => api<DirectoryGroup[]>(`/organizations/${orgId}/directory-groups`),
  })

const toAddresses = (list: string[]) => list.map((address) => ({ address }))

function toValues(target: Alias | Kind, people: Member[]): Values {
  if (typeof target === "string") return { ...blank, kind: target }
  const toPeople = (ids: string[]) =>
    ids.map((id) => {
      const person = people.find((p) => p.userId === id)
      return person ? { name: person.displayName || undefined, address: person.email } : { address: id }
    })
  return {
    kind: target.kind,
    domainId: target.domainId,
    localPart: target.localPart,
    name: target.name,
    destinations: toAddresses(target.destinations),
    active: target.active,
    acceptFrom: target.acceptFrom,
    allowedSenders: toAddresses(target.allowedSenders),
    sentMailboxId: target.sentMailboxId ?? "",
    sendAs: toAddresses(target.sendAs),
    owners: toPeople(target.owners),
    groups: target.groups,
    memberDomains: target.memberDomains,
    moderated: target.moderated,
    moderators: toPeople(target.moderators),
    moderationExempt: toAddresses(target.moderationExempt),
    hidden: target.hiddenFromDirectory,
  }
}

export function AliasesTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const session = useSession().data
  const canReadAliases = hasPerm(session, orgId, "aliases:read")
  const canWriteAliases = hasPerm(session, orgId, "aliases:write")
  const canWriteLists = hasPerm(session, orgId, "aliases:write", "lists:write")
  const aliases = useQuery({ ...aliasesQuery(orgId), enabled: canReadAliases, select: (all) => all.filter((a) => a.kind === "alias" && !a.personalOwnerId) })
  const lists = useQuery({ ...aliasesQuery(orgId), select: (all) => all.filter((a) => a.kind === "list") })
  const personal = useQuery({ ...aliasesQuery(orgId), select: (all) => all.filter((a) => a.personalOwnerId) })
  const members = useQuery(membersQuery(orgId))
  const domains = useQuery(domainsQuery(orgId))
  const groups = useQuery(directoryGroupsQuery(orgId))
  const [editing, setEditing] = useState<Alias | Kind | null>(null)
  const [viewing, setViewing] = useState<Alias | null>(null)
  const [importingCsv, setImportingCsv] = useState<Kind | null>(null)
  const [deleting, setDeleting] = useState<Alias | null>(null)
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: blank })
  const [kind, acceptFrom, moderated] = useWatch({ control: form.control, name: ["kind", "acceptFrom", "moderated"] })
  const shared = useQuery({ ...mailboxesQuery(orgId), enabled: !!editing && kind === "list", select: (items) => items.filter((m) => m.kind === "shared") })
  const errors = form.formState.errors
  const existing = typeof editing === "object" ? editing : null

  const domainName = (id: string) => domains.data?.find((d) => d.id === id)?.name ?? ""
  const label = (a: Alias) => (a.localPart ? a.address : `Any address @${domainName(a.domainId)}`)

  async function open(target: Alias | Kind) {
    try {
      const targetKind = typeof target === "string" ? target : target.kind
      const people = targetKind === "list" && canWriteLists ? await qc.ensureQueryData(membersQuery(orgId)) : []
      form.reset(toValues(target, people))
      setEditing(target)
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }

  async function exportLists() {
    try {
      const people = await qc.ensureQueryData(membersQuery(orgId))
      downloadCsv("distribution-lists.csv", exportListsCsv(lists.data ?? [], people))
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }

  const save = useMutation({
    mutationFn: async (v: Values) => {
      const list = v.kind === "list"
      const people = list ? await qc.ensureQueryData(membersQuery(orgId)) : []
      const userIds = (entries: Values["owners"], code: string) => {
        const resolved = entries.map((o) => ({ ...o, id: people.find((p) => p.email.toLowerCase() === o.address.toLowerCase())?.userId }))
        const outside = resolved.filter((o) => !o.id).map((o) => o.address)
        if (outside.length) {
          throw new ApiError(422, code, `${outside.join(", ")} ${outside.length === 1 ? "isn’t a member" : "aren’t members"} of this organization. Add them under Members first.`)
        }
        return resolved.map((o) => o.id)
      }
      const owners = list ? userIds(v.owners, "invalid_owners") : []
      const moderated = list && v.moderated
      const body = {
        kind: v.kind,
        name: list ? v.name : undefined,
        destinations: v.destinations.map((a) => a.address),
        acceptFrom: list ? v.acceptFrom : "anyone",
        allowedSenders: list && v.acceptFrom === "allowlist" ? v.allowedSenders.map((a) => a.address) : [],
        sentMailboxId: list ? v.sentMailboxId : "",
        sendAs: list ? v.sendAs.map((a) => a.address) : [],
        owners,
        groups: list ? v.groups : [],
        memberDomains: list ? v.memberDomains : [],
        moderated,
        moderators: moderated ? userIds(v.moderators, "invalid_moderators") : [],
        moderationExempt: moderated ? v.moderationExempt.map((a) => a.address) : [],
        hiddenFromDirectory: list && v.hidden,
      }
      return existing
        ? api<Alias>(`/aliases/${existing.id}`, { method: "PATCH", body: { ...body, active: v.active } })
        : api<Alias>(`/organizations/${orgId}/aliases`, { method: "POST", body: { ...body, domainId: v.domainId, localPart: v.localPart } })
    },
    meta: { silent: true },
    onSuccess: (saved) => {
      toast.success(existing ? `Saved ${label(saved)}` : `Created ${label(saved)}`)
      qc.invalidateQueries({ queryKey: ["aliases", orgId] })
      qc.invalidateQueries({ queryKey: ["list-members", saved.id] })
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
      {a.kind === "list" && (
        <IconButton label={`Show the members of ${label(a)}`} size="icon-sm" onClick={() => setViewing(a)}>
          <ListTree />
        </IconButton>
      )}
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
      {canReadAliases && (
        <section className="flex flex-col gap-6">
          <SectionHeader
            title="Aliases"
            description="Forward mail for an address to up to 100 destinations. Every mailbox among them can send as the alias. A catch-all receives mail for addresses that have no mailbox or alias."
            actions={
              <>
                <Button
                  variant="secondary"
                  disabled={!aliases.data?.length}
                  onClick={() => downloadCsv("aliases.csv", exportAliasesCsv(aliases.data ?? []))}
                >
                  <Download aria-hidden />
                  Export CSV
                </Button>
                {canWriteAliases && (
                  <>
                    <Button variant="secondary" onClick={() => setImportingCsv("alias")}>
                      <Upload aria-hidden />
                      Import CSV
                    </Button>
                    <Button onClick={() => open("alias")}>
                      <Plus aria-hidden />
                      Create alias
                    </Button>
                  </>
                )}
              </>
            }
          />
          <QueryView query={aliases} empty={<EmptyState icon={Forward} title="No aliases" description="Create an alias to forward an address without a separate mailbox." />}>
            {(items) => (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Address</TableHead>
                    <TableHead>Destinations</TableHead>
                    {canWriteAliases && (
                      <TableHead className="w-20">
                        <span className="sr-only">Actions</span>
                      </TableHead>
                    )}
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
                      {canWriteAliases && <TableCell>{actions(a)}</TableCell>}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </QueryView>
        </section>
      )}

      <section className="flex flex-col gap-6">
        <SectionHeader
          title="Distribution lists"
          description="One address for a group of up to 5,000 people, listed in the directory. Choose who can send to it, who can send as it, and who can change its members without being an administrator."
          actions={
            <>
              <Button variant="secondary" disabled={!lists.data?.length} onClick={() => void exportLists()}>
                <Download aria-hidden />
                Export CSV
              </Button>
              {canWriteLists && (
                <>
                  <Button variant="secondary" onClick={() => setImportingCsv("list")}>
                    <Upload aria-hidden />
                    Import CSV
                  </Button>
                  <Button variant="secondary" onClick={() => open("list")}>
                    <Plus aria-hidden />
                    Create list
                  </Button>
                </>
              )}
            </>
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
                  {canWriteLists && (
                    <TableHead className="w-28">
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  )}
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
                        {a.moderated && <StatusBadge tone="info">Moderated</StatusBadge>}
                        {!a.active && <StatusBadge tone="neutral">Inactive</StatusBadge>}
                        {a.hiddenFromDirectory && <StatusBadge tone="neutral">Hidden</StatusBadge>}
                      </div>
                    </TableCell>
                    <TableCell>{[plural(a.destinations.length, "member"), a.groups.length && plural(a.groups.length, "group"), a.memberDomains.length && plural(a.memberDomains.length, "domain")].filter(Boolean).join(" · ")}</TableCell>
                    <TableCell>{senders[a.acceptFrom].label}</TableCell>
                    {canWriteLists && <TableCell>{actions(a)}</TableCell>}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </QueryView>
      </section>

      <section className="flex flex-col gap-6">
        <PersonalAliasPolicyHeader orgId={orgId} />
        <QueryView
          query={personal}
          empty={<EmptyState icon={VenetianMask} title="No personal aliases" description="Aliases that people create for themselves appear here, so you can see who owns each one and delete any that shouldn’t exist." />}
        >
          {(items) => (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Alias and owner</TableHead>
                  <TableHead className="w-12">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((a) => {
                  const owner = members.data?.find((m) => m.userId === a.personalOwnerId)
                  return (
                    <TableRow key={a.id}>
                      <TableCell className="whitespace-normal">
                        <div className="flex min-w-0 flex-col gap-1">
                          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                            <span className="font-mono text-sm break-all">{a.address}</span>
                            {expired(a) ? <StatusBadge tone="neutral">Expired</StatusBadge> : !a.active && <StatusBadge tone="neutral">Off</StatusBadge>}
                          </div>
                          <span className="text-sm break-words text-fg-tertiary">
                            {owner ? (owner.displayName ? `${owner.displayName} · ${owner.email}` : owner.email) : members.data && "No longer a member"}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell>
                        <IconButton label={`Delete ${a.address}`} size="icon-sm" onClick={() => setDeleting(a)}>
                          <Trash2 />
                        </IconButton>
                      </TableCell>
                    </TableRow>
                  )
                })}
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
        {existing?.localPart && canWriteAliases && (
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
              hint={kind === "list" ? "Added by hand: mailboxes, other lists, organization contacts or outside addresses. Press Enter or type a comma after each one." : "Press Enter or type a comma after each address."}
              error={fieldState.error?.message}
            >
              <RecipientInput id="alias-destinations" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
            </Field>
          )}
        />
        {kind === "list" && (
          <>
            <Controller
              control={form.control}
              name="groups"
              render={({ field, fieldState }) => (
                <Field id="alias-groups" label="Groups" hint="Groups from your identity provider. People join and leave the list as they join and leave the group." error={fieldState.error?.message}>
                  <MultiSelect
                    id="alias-groups"
                    value={field.value}
                    onChange={field.onChange}
                    invalid={!!fieldState.error}
                    placeholder={groups.isPending ? "Loading groups…" : groups.isError ? "Groups could not be loaded" : groups.data?.length ? "Choose groups" : "No groups provisioned over SCIM yet"}
                    options={(groups.data ?? []).map((g) => ({ value: g.id, label: g.name, hint: `${g.provider} · ${plural(g.members, "member")} here` }))}
                  />
                </Field>
              )}
            />
            <Controller
              control={form.control}
              name="memberDomains"
              render={({ field, fieldState }) => (
                <Field id="alias-member-domains" label="Everyone on these domains" hint="Every active personal mailbox on the domains you choose, including mailboxes created later." error={fieldState.error?.message}>
                  <MultiSelect
                    id="alias-member-domains"
                    value={field.value}
                    onChange={field.onChange}
                    invalid={!!fieldState.error}
                    placeholder={domains.isPending ? "Loading domains…" : domains.data?.length ? "Choose domains" : "Add a domain first"}
                    options={(domains.data ?? []).map((d) => ({ value: d.id, label: `@${d.name}` }))}
                  />
                </Field>
              )}
            />
          </>
        )}
        {existing && <SwitchField control={form.control} name="active" id="alias-active" label="Active" hint="Turn it off to stop forwarding mail without deleting it." />}
        {kind === "list" && (
          <>
            <SwitchField
              control={form.control}
              name="hidden"
              id="alias-hidden"
              label="Hide from directory"
              hint="The list still receives mail, but people won’t find it in the directory, address suggestions or synced address books."
            />
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
              name="sentMailboxId"
              render={({ field, fieldState }) => (
                <Field
                  id="list-sent"
                  label="Save sent copies in"
                  hint="Where Aether Mail files messages sent as the list. Pick a shared mailbox so everyone who sends as the list sees one history."
                  error={fieldState.error?.message ?? (shared.isError ? "Shared mailboxes could not be loaded. Close and reopen the list to try again." : undefined)}
                >
                  <Select value={shared.isPending ? "" : field.value || "personal"} disabled={shared.isPending} onValueChange={(v) => field.onChange(v === "personal" ? "" : v)}>
                    <SelectTrigger id="list-sent" className="w-full">
                      <SelectValue placeholder="Loading shared mailboxes…" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="personal">Each sender’s own Sent folder</SelectItem>
                      {shared.data?.map((m) => (
                        <SelectItem key={m.id} value={m.id}>
                          {m.address}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
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
            <SwitchField
              control={form.control}
              name="moderated"
              id="alias-moderated"
              label="Hold messages for approval"
              hint="A moderator approves each message before it reaches the members. Messages nobody approves within 5 days are discarded, and the sender is told."
            />
            {moderated && (
              <>
                <Controller
                  control={form.control}
                  name="moderators"
                  render={({ field, fieldState }) => (
                    <Field id="alias-moderators" label="Moderators" hint="People in this organization who approve messages from Settings. Leave empty to let the owners approve." error={fieldState.error?.message}>
                      <RecipientInput id="alias-moderators" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
                    </Field>
                  )}
                />
                <Controller
                  control={form.control}
                  name="moderationExempt"
                  render={({ field, fieldState }) => (
                    <Field id="alias-exempt" label="Senders who skip approval" hint="Addresses, or @example.com for everyone at a domain. Moderators and owners always skip approval." error={fieldState.error?.message}>
                      <RecipientInput id="alias-exempt" value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="@example.com" allowDomains />
                    </Field>
                  )}
                />
              </>
            )}
          </>
        )}
      </FormDialog>

      {viewing && <ResolvedMembersDialog list={viewing} onClose={() => setViewing(null)} />}

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={deleting?.kind === "list" ? "Delete distribution list?" : "Delete alias?"}
        description={deleting ? (deleting.personalOwnerId ? `Mail to ${deleting.address} is rejected from now on, and its owner gets a place back under their limit.` : `Mail to ${label(deleting)} stops being forwarded immediately.`) : ""}
        confirmLabel={deleting?.kind === "list" ? "Delete list" : "Delete alias"}
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />

      {importingCsv && (
        <CsvImportDialog
          orgId={orgId}
          kind={importingCsv}
          open
          onOpenChange={(open) => !open && setImportingCsv(null)}
          defaultDomain={domains.data?.[0]?.name}
          onCompleted={() => qc.invalidateQueries({ queryKey: ["aliases", orgId] })}
        />
      )}
    </div>
  )
}

function ResolvedMembersDialog({ list, onClose }: { list: Alias; onClose: () => void }) {
  const members = useQuery({ queryKey: ["list-members", list.id], queryFn: () => api<ResolvedMember[]>(`/aliases/${list.id}/members`) })
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-[720px]">
        <DialogHeader>
          <DialogTitle>{list.name || "Members"}</DialogTitle>
          <DialogDescription>
            Who receives mail sent to <span className="font-mono">{list.address}</span> right now, including people from groups and domains. Lists among the members are expanded when mail is delivered.
          </DialogDescription>
        </DialogHeader>
        <QueryView query={members} empty={<EmptyState icon={Users} title="No members right now" description="Nobody receives mail sent to this list. Add members, a group with people in it, or a domain." />}>
          {(items) => (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Member</TableHead>
                  <TableHead>Through</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((m) => (
                  <TableRow key={m.address}>
                    <TableCell>
                      <div className="flex min-w-0 flex-col">
                        <span className="truncate font-medium">{m.name || m.address}</span>
                        {m.name && <span className="truncate font-mono text-sm text-fg-tertiary">{m.address}</span>}
                      </div>
                    </TableCell>
                    <TableCell className="whitespace-normal">
                      <div className="flex flex-wrap gap-1">
                        {m.direct && <StatusBadge tone="neutral">Added by hand</StatusBadge>}
                        {m.groups.map((g) => (
                          <StatusBadge key={g} tone="info">
                            {g}
                          </StatusBadge>
                        ))}
                        {m.domain && <StatusBadge tone="info">@{m.address.split("@")[1]}</StatusBadge>}
                      </div>
                    </TableCell>
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

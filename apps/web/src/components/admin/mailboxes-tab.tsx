"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArchiveRestore, ArrowRightLeft, BellRing, CalendarArrowUp, Download, Ellipsis, Eye, EyeOff, HardDrive, Inbox, KeyRound, ListFilter, Plug, Pencil, Plus, ShieldCheck, RotateCcw, Shield, RefreshCw, Trash2, Upload, Users } from "lucide-react"
import { useRef, useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { CsvImportDialog } from "@/components/admin/csv-import-dialog"
import { DomainSelect } from "@/components/admin/domain-select"
import { LastSignIn } from "@/components/admin/usage-cells"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { MailboxAccessReport } from "@/components/mailbox-access-report"
import { LockNote } from "@/components/notice"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { RecoverDialog } from "@/components/mail/recover-dialog"
import { ExportMailboxDialog } from "@/components/settings/export-mail"
import { MailboxRules } from "@/components/settings/mail-rules"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { tierText, UsageBar, usagePercent } from "@/components/usage-bar"
import { useSession } from "@/hooks/use-session"
import { api, ApiError, errorMessage, limitMessage } from "@/lib/api"
import { downloadCsv, exportMailboxesCsv } from "@/lib/csv"
import { formatBytes, formatDateTime, megabytes, plural } from "@/lib/format"
import { storageTier } from "@/lib/mail"
import { canReadDelegates, grantHint, hasPerm, platformOnly } from "@/lib/permissions"
import { domainDefaultsQuery, domainsQuery, featuresQuery, limitsQuery, mailboxesQuery, membersQuery, myMailboxesQuery, organizationsQuery } from "@/lib/queries"
import type { Delegate, DelegateAccess, ImportCounts, Mailbox, MailboxAppPassword, Organization, OrganizationLimits, Protocol, QuotaDefault, QuotaWarnings } from "@/lib/types"

const MB = 1024 * 1024

const imports = {
  calendar: { path: "calendar/import", accept: ".ics,text/calendar", noun: "event" },
  contacts: { path: "contacts/import", accept: ".vcf,text/vcard", noun: "contact" },
}

type ImportKind = keyof typeof imports

const schema = z
  .object({
    kind: z.enum(["user", "shared"]),
    domainId: z.string().min(1, "Choose a domain."),
    localPart: z
      .string()
      .trim()
      .toLowerCase()
      .regex(/^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$/, "Use letters, digits, dots, hyphens or underscores. A plus sign marks a tag, so it can’t be part of an address."),
    displayName: z.string().trim(),
    password: z.union([z.literal(""), z.string().min(12, "Use at least 12 characters, or leave it empty.")]),
    quotaMb: z.string().regex(/^\d*$/, "Enter a whole number of megabytes."),
  })
  .refine((v) => v.kind === "user" || v.password === "", { message: "Shared mailboxes have no password.", path: ["password"] })

type Values = z.infer<typeof schema>

const editSchema = z.object({
  displayName: z.string().trim(),
  quotaMb: z.string().regex(/^\d*$/, "Enter a whole number of megabytes."),
})

type EditValues = z.infer<typeof editSchema>

const kinds = [
  { value: "user" as const, label: "User mailbox" },
  { value: "shared" as const, label: "Shared mailbox" },
]

const storageErrors = ["quota_required", "storage_limit_reached"]

function storageRoom(l: OrganizationLimits, current?: number) {
  const room = formatBytes(Math.max(l.maxStorageBytes - l.usage.storageBytes, 0) + (current ?? 0))
  const limit = formatBytes(l.maxStorageBytes)
  return current === undefined ? `${room} of this organization's ${limit} storage limit is left.` : `Up to ${room} fits in this organization's ${limit} storage limit.`
}

function quotaHint(l: OrganizationLimits | undefined, defaults: QuotaDefault | undefined) {
  const room = l?.maxStorageBytes ? ` ${storageRoom(l)}` : ""
  if (!defaults) return `Leave empty to use the default quota.${room}`
  if (defaults.effectiveQuotaBytes) return `Leave empty to use the default, ${formatBytes(defaults.effectiveQuotaBytes)}${room ? "." : ", or enter 0 for no quota."}${room}`
  return room ? `Every mailbox needs a quota.${room}` : "Leave empty for no quota."
}

const delegateSchema = z.object({
  email: z.email("Enter a valid email address."),
  access: z.enum(["full", "read"]),
  sendAs: z.boolean(),
  sendOnBehalf: z.boolean(),
})

const accessOptions = [
  { value: "full" as const, label: "Full access" },
  { value: "read" as const, label: "Read only" },
]

type DelegateValues = z.infer<typeof delegateSchema>

const rights = ({ email, access, sendAs, sendOnBehalf }: DelegateValues): DelegateValues => ({ email, access, sendAs, sendOnBehalf })

const rightsLabel = (d: DelegateValues) => [d.access === "read" ? "read only" : "full access", d.sendAs && "send as", d.sendOnBehalf && "send on behalf"].filter(Boolean).join(", ")

function DelegatesDialog({ mailbox, canManage, how, onOpenChange }: { mailbox: Mailbox | null; canManage: boolean; how: string; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient()
  const session = useSession().data
  const self = mailbox && platformOnly(session, mailbox.orgId, "domain_admin", "custom") ? session?.user : undefined
  const key = ["delegates", mailbox?.id]
  const shared = mailbox?.kind === "shared"
  const delegates = useQuery({ queryKey: key, queryFn: () => api<Delegate[]>(`/mailboxes/${mailbox?.id}/delegates`), enabled: !!mailbox })
  const [removing, setRemoving] = useState<Delegate | null>(null)
  const form = useForm<DelegateValues>({ resolver: zodResolver(delegateSchema), defaultValues: { email: "", access: "full", sendAs: false, sendOnBehalf: false } })
  const post = (values: DelegateValues) => api<Delegate>(`/mailboxes/${mailbox?.id}/delegates`, { method: "POST", body: values })

  const add = useMutation({
    mutationFn: post,
    meta: { silent: true },
    onSuccess: (d) => {
      toast.success(`${d.email}: ${rightsLabel(d)}`)
      form.reset()
      qc.invalidateQueries({ queryKey: key })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 422) form.setError("email", { message: error.message })
      else toast.error(errorMessage(error))
    },
  })
  const change = useMutation({
    mutationFn: post,
    onSuccess: (d) => {
      toast.success(`${d.email}: ${rightsLabel(d)}`)
      return qc.invalidateQueries({ queryKey: key })
    },
  })
  const remove = useMutation({
    mutationFn: (d: Delegate) => api(`/mailboxes/${mailbox?.id}/delegates/${d.userId}`, { method: "DELETE" }),
    onSuccess: (_d, d) => {
      toast.success(`Removed ${d.email}`)
      setRemoving(null)
      qc.invalidateQueries({ queryKey: key })
    },
  })

  const title = shared ? `Members of ${mailbox?.address}` : `Delegates for ${mailbox?.address}`
  const description = shared
    ? "Full access lets a member read, organize and file mail. Send as and Send on behalf are separate: Send as sends from this address; Send on behalf shows the member's name as the sender."
    : "Full access lets a delegate read and organize this mailbox; Send as additionally allows sending from the address and creating app passwords for it. Read only allows reading in Aether Mail and nothing else."

  if (!canManage) {
    return (
      <Dialog open={!!mailbox} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>{description}</DialogDescription>
          </DialogHeader>
          <QueryView query={delegates} skeleton={<RowsSkeleton rows={2} />} empty={<p className="text-sm text-fg-tertiary">{shared ? "No members yet." : "No delegates yet."}</p>}>
            {(items) => (
              <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
                {items.map((d) => (
                  <li key={d.userId} className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
                    <div className="flex min-w-0 flex-col">
                      <span className="truncate font-medium">{d.displayName || d.email}</span>
                      {d.displayName && <span className="truncate font-mono text-sm text-fg-tertiary">{d.email}</span>}
                    </div>
                    <StatusBadge tone="neutral">{rightsLabel(d)}</StatusBadge>
                  </li>
                ))}
              </ul>
            )}
          </QueryView>
        </DialogContent>
      </Dialog>
    )
  }

  return (
    <>
      <FormDialog
        open={!!mailbox}
        onOpenChange={(open) => {
          onOpenChange(open)
          if (!open) form.reset()
        }}
        title={title}
        description={description}
        submitLabel={shared ? "Add member" : "Add delegate"}
        cancelLabel="Done"
        pending={add.isPending}
        onSubmit={form.handleSubmit((v) =>
          v.email.toLowerCase() === self?.email.toLowerCase() ? form.setError("email", { message: `You need the owner or administrator role in this organization to add yourself. ${how}` }) : add.mutate(v)
        )}
      >
        <QueryView query={delegates} skeleton={<RowsSkeleton rows={2} />} empty={<p className="text-sm text-fg-tertiary">{shared ? "No members yet." : "No delegates yet."}</p>}>
          {(items) => (
            <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
              {items.map((d) => {
                const shown = change.isPending && change.variables.email === d.email ? change.variables : d
                return (
                  <li key={d.userId} className="flex flex-col gap-3 px-4 py-3">
                    <div className="flex items-center gap-3">
                      <div className="flex min-w-0 flex-1 flex-col">
                        <span className="truncate">{d.displayName || d.email}</span>
                        {d.displayName && <span className="truncate font-mono text-sm text-fg-tertiary">{d.email}</span>}
                      </div>
                      <Select value={shown.access} disabled={change.isPending || d.userId === self?.id} onValueChange={(access) => change.mutate({ ...rights(d), access: access as DelegateAccess })}>
                        <SelectTrigger aria-label={`Access for ${d.email}`} className="w-36">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {accessOptions.map((o) => (
                            <SelectItem key={o.value} value={o.value}>
                              {o.label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <IconButton type="button" label={`Remove ${d.email}`} size="icon-sm" onClick={() => setRemoving(d)}>
                        <Trash2 />
                      </IconButton>
                    </div>
                    <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
                      <div className="flex items-center gap-2">
                        <Switch
                          id={`send-as-${d.userId}`}
                          checked={shown.sendAs}
                          disabled={change.isPending || d.userId === self?.id}
                          onCheckedChange={(sendAs) => change.mutate({ ...rights(d), sendAs })}
                          aria-label={`Send as for ${d.email}`}
                        />
                        <Label htmlFor={`send-as-${d.userId}`}>Send as</Label>
                      </div>
                      <div className="flex items-center gap-2">
                        <Switch
                          id={`send-on-behalf-${d.userId}`}
                          checked={shown.sendOnBehalf}
                          disabled={change.isPending || d.userId === self?.id}
                          onCheckedChange={(sendOnBehalf) => change.mutate({ ...rights(d), sendOnBehalf })}
                          aria-label={`Send on behalf for ${d.email}`}
                        />
                        <Label htmlFor={`send-on-behalf-${d.userId}`}>Send on behalf</Label>
                      </div>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
        </QueryView>
        <Field
          id="delegate-email"
          label="Email"
          hint={`The email of an active person in this organization.${self ? " Platform administrators need the owner or administrator role here to add themselves or change their own access." : ""}`}
          error={form.formState.errors.email?.message}
        >
          <Input id="delegate-email" type="email" autoComplete="off" spellCheck={false} aria-invalid={!!form.formState.errors.email} {...form.register("email")} />
        </Field>
        <Controller
          control={form.control}
          name="access"
          render={({ field }) => <SegmentedControl label="Access" options={accessOptions} value={field.value} onChange={field.onChange} />}
        />
        <SwitchField control={form.control} name="sendAs" id="delegate-send-as" label="Send as" />
        <SwitchField control={form.control} name="sendOnBehalf" id="delegate-send-on-behalf" label="Send on behalf" />
      </FormDialog>

      <ConfirmDialog
        open={!!removing}
        onOpenChange={(open) => !open && setRemoving(null)}
        title={`Remove ${removing?.email}?`}
        description={
          shared
            ? `They lose access to ${mailbox?.address} in Aether Mail and in their mail apps right away.`
            : `They lose access to ${mailbox?.address}, and the app passwords they created for it stop working.`
        }
        confirmLabel={shared ? "Remove member" : "Remove delegate"}
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing)}
      />
    </>
  )
}

function RetentionSetting({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const current = useQuery({ ...organizationsQuery, select: (orgs) => orgs.find((o) => o.id === orgId)?.deletedItemRetentionDays }).data
  const [days, setDays] = useState<string | null>(null)
  const value = days ?? (current === undefined ? "" : String(current))
  const valid = /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= 30
  const save = useMutation({
    mutationFn: () => api<Organization>(`/organizations/${orgId}`, { method: "PATCH", body: { deletedItemRetentionDays: Number(value) } }),
    onSuccess: (org) => {
      toast.success(`Deleted messages are now kept for ${plural(org.deletedItemRetentionDays, "day")}`)
      setDays(null)
      qc.invalidateQueries({ queryKey: ["organizations"] })
      qc.invalidateQueries({ queryKey: ["recoverable"] })
    },
  })

  return (
    <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <SectionHeader
        title="Deleted item retention"
        description="How long messages stay recoverable after they are permanently deleted, for example by emptying Trash. People restore them from the Trash menu in Aether Mail, and administrators from a mailbox's More actions menu."
      />
      <form
        noValidate
        onSubmit={(e) => {
          e.preventDefault()
          if (valid) save.mutate()
        }}
      >
        <Field id="retention-days" label="Days" hint="From 1 to 30. Kept messages do not count towards the mailbox quota." error={value && !valid ? "Enter a whole number of days from 1 to 30." : undefined}>
          <div className="flex flex-col gap-3 sm:flex-row">
            <Input
              id="retention-days"
              inputMode="numeric"
              autoComplete="off"
              className="sm:max-w-32"
              value={value}
              disabled={current === undefined}
              aria-invalid={!!value && !valid}
              onChange={(e) => setDays(e.target.value)}
            />
            <Button type="submit" variant="secondary" loading={save.isPending} disabled={!valid || Number(value) === current}>
              Save retention
            </Button>
          </div>
        </Field>
      </form>
    </section>
  )
}

const protocolOptions: { value: Protocol; label: string; hint: string }[] = [
  { value: "webmail", label: "Aether Mail", hint: "Read and send mail in the browser." },
  { value: "imap", label: "IMAP", hint: "Mail apps read and organize mail." },
  { value: "smtp", label: "SMTP submission", hint: "Mail apps send mail." },
  { value: "sieve", label: "ManageSieve", hint: "Mail apps edit filters." },
  { value: "dav", label: "CalDAV and CardDAV", hint: "Calendar and contacts apps sync." },
]

function ProtocolsDialog({
  initial,
  title,
  description,
  pending,
  onOpenChange,
  onSave,
}: {
  initial: Protocol[] | null
  title: string
  description: string
  pending: boolean
  onOpenChange: (open: boolean) => void
  onSave: (protocols: Protocol[]) => void
}) {
  const [chosen, setChosen] = useState<Protocol[]>(initial ?? [])

  return (
    <FormDialog
      open={!!initial}
      onOpenChange={onOpenChange}
      title={title}
      description={description}
      submitLabel="Save"
      pending={pending}
      onSubmit={(e) => {
        e.preventDefault()
        onSave(protocolOptions.map((o) => o.value).filter((p) => chosen.includes(p)))
      }}
    >
      {protocolOptions.map((o) => (
        <div key={o.value} className="flex items-start gap-3">
          <Switch
            id={`protocol-${o.value}`}
            checked={chosen.includes(o.value)}
            onCheckedChange={(on) => setChosen((list) => (on ? [...list, o.value] : list.filter((p) => p !== o.value)))}
            className="mt-1"
          />
          <div className="flex flex-col gap-1">
            <Label htmlFor={`protocol-${o.value}`}>{o.label}</Label>
            <p className="text-sm text-fg-tertiary">{o.hint}</p>
          </div>
        </div>
      ))}
    </FormDialog>
  )
}

const warningLevel = z.string().trim().regex(/^([5-9]\d)?$/, "Enter a whole number from 50 to 99, or leave it empty.")

const warningsSchema = z
  .object({ first: warningLevel, second: warningLevel, notifyAdmins: z.boolean() })
  .refine((v) => !v.first || !v.second || Number(v.second) > Number(v.first), { message: "Set the final warning above the first one.", path: ["second"] })

function QuotaWarningsDialog({ orgId, warnings, onClose }: { orgId: string; warnings: QuotaWarnings; onClose: () => void }) {
  const qc = useQueryClient()
  const form = useForm({
    resolver: zodResolver(warningsSchema),
    defaultValues: { first: String(warnings.percents[0] ?? ""), second: String(warnings.percents[1] ?? ""), notifyAdmins: warnings.notifyAdmins },
  })
  const errors = form.formState.errors
  const save = useMutation({
    mutationFn: ({ first, second, notifyAdmins }: z.infer<typeof warningsSchema>) =>
      api<QuotaWarnings>(`/organizations/${orgId}/quota-warnings`, { method: "PUT", body: { percents: [first, second].filter(Boolean).map(Number), notifyAdmins } }),
    onSuccess: (saved) => {
      toast.success(saved.percents.length ? `Storage warnings at ${saved.percents.map((p) => `${p}%`).join(" and ")}` : "Storage warnings turned off")
      qc.invalidateQueries({ queryKey: ["quota-warnings", orgId] })
      qc.invalidateQueries({ queryKey: ["my-mailboxes"] })
      onClose()
    },
  })

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title="Storage warnings"
      description="Aether measures every mailbox every few minutes and emails it once as it passes each level, so people can clean up before mail is returned to senders. Clear both levels to turn warnings off."
      submitLabel="Save warnings"
      pending={save.isPending}
      onSubmit={form.handleSubmit((v) => save.mutate(v))}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field id="warn-first" label="First warning, % full" hint="For example 80." error={errors.first?.message}>
          <Input id="warn-first" inputMode="numeric" autoComplete="off" aria-invalid={!!errors.first} {...form.register("first")} />
        </Field>
        <Field id="warn-second" label="Final warning, % full" hint="Optional. For example 95." error={errors.second?.message}>
          <Input id="warn-second" inputMode="numeric" autoComplete="off" aria-invalid={!!errors.second} {...form.register("second")} />
        </Field>
      </div>
      <SwitchField control={form.control} name="notifyAdmins" id="warn-admins" label="Email administrators too" hint="Owners and administrators of this organization get a copy in their own mailbox." />
    </FormDialog>
  )
}

function MailboxStorage({ mailbox, levels }: { mailbox: Mailbox; levels?: number[] }) {
  const used = mailbox.usedBytes ?? 0
  if (!mailbox.quotaBytes) {
    return (
      <span className="text-sm whitespace-nowrap tabular-nums">
        {formatBytes(used)} <span className="text-fg-tertiary">· No limit</span>
      </span>
    )
  }
  const tier = storageTier(used, mailbox.quotaBytes, levels)
  return (
    <div className="flex w-40 flex-col gap-2">
      <span className="flex justify-between gap-2 text-sm whitespace-nowrap tabular-nums">
        <span>
          {formatBytes(used)} <span className="text-fg-tertiary">of {formatBytes(mailbox.quotaBytes)}</span>
        </span>
        <span className={tierText[tier]}>{usagePercent(used, mailbox.quotaBytes)}%</span>
      </span>
      <UsageBar used={used} quota={mailbox.quotaBytes} tier={tier} />
    </div>
  )
}

function AppPasswordsDialog({ mailbox, canRevoke, onOpenChange }: { mailbox: Mailbox | null; canRevoke: boolean; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient()
  const key = ["mailbox-app-passwords", mailbox?.id]
  const passwords = useQuery({
    queryKey: key,
    queryFn: () => api<MailboxAppPassword[]>(`/mailboxes/${mailbox?.id}/app-passwords`),
    enabled: !!mailbox,
  })
  const [revoking, setRevoking] = useState<MailboxAppPassword | null>(null)
  const revoke = useMutation({
    mutationFn: (p: MailboxAppPassword) => api(`/mailboxes/${mailbox?.id}/app-passwords/${p.id}`, { method: "DELETE" }),
    onSuccess: (_d, p) => {
      toast.success(`Revoked “${p.name}”`)
      setRevoking(null)
      qc.invalidateQueries({ queryKey: key })
    },
  })

  return (
    <>
      <Dialog open={!!mailbox} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>App passwords for {mailbox?.address}</DialogTitle>
            <DialogDescription>
              Passwords used by mail, calendar and contact apps over IMAP, SMTP, CalDAV and CardDAV. Revoking one disconnects that app immediately.
            </DialogDescription>
          </DialogHeader>
          <QueryView
            query={passwords}
            skeleton={<RowsSkeleton rows={2} />}
            empty={<EmptyState icon={KeyRound} title="No app passwords" description="No app passwords are active for this mailbox." />}
          >
            {(items) => (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Created by</TableHead>
                    <TableHead>Created</TableHead>
                    {canRevoke && (
                      <TableHead className="w-12">
                        <span className="sr-only">Actions</span>
                      </TableHead>
                    )}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((p) => (
                    <TableRow key={p.id}>
                      <TableCell className="font-medium">{p.name}</TableCell>
                      <TableCell className="font-mono text-sm">{p.userEmail}</TableCell>
                      <TableCell>{formatDateTime(p.createdAt)}</TableCell>
                      {canRevoke && (
                        <TableCell>
                          <IconButton label={`Revoke ${p.name}`} size="icon-sm" onClick={() => setRevoking(p)}>
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
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(open) => !open && setRevoking(null)}
        title={`Revoke “${revoking?.name}”?`}
        description={`Any mail or PIM client using this app password for ${mailbox?.address} will immediately stop authenticating.`}
        confirmLabel="Revoke password"
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking)}
      />
    </>
  )
}

function QuotaDialog({
  mailbox,
  limits,
  quotaError,
  onSaved,
  onClose,
}: {
  mailbox: Mailbox
  limits?: OrganizationLimits
  quotaError: (error: ApiError, current: number) => Promise<string>
  onSaved: () => void
  onClose: () => void
}) {
  const saved = mailbox.quotaBytes ? megabytes(mailbox.quotaBytes) : ""
  const [value, setValue] = useState(saved)
  const [error, setError] = useState("")

  const save = useMutation({
    mutationFn: () => api<Mailbox>(`/mailboxes/${mailbox.id}`, { method: "PATCH", body: { quotaBytes: Number(value) * MB } }),
    meta: { silent: true },
    onSuccess: (m) => {
      toast.success(m.quotaBytes ? `${m.address} now has a quota of ${formatBytes(m.quotaBytes)}` : `${m.address} now has no quota`)
      onSaved()
      onClose()
    },
    onError: async (e) => {
      if (e instanceof ApiError && storageErrors.includes(e.code)) setError(await quotaError(e, mailbox.quotaBytes))
      else toast.error(errorMessage(e))
    },
  })

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title="Change quota"
      description={<span className="font-mono">{mailbox.address}</span>}
      submitLabel="Save quota"
      pending={save.isPending}
      onSubmit={(e) => {
        e.preventDefault()
        if (!/^\d*$/.test(value)) return setError("Enter a whole number of megabytes.")
        if (value === saved) return onClose()
        save.mutate()
      }}
    >
      <Field id="quota-mb" label="Quota in MB" hint={`Uses ${formatBytes(mailbox.usedBytes ?? 0)} now. ${limits?.maxStorageBytes ? `Every mailbox needs a quota. ${storageRoom(limits, mailbox.quotaBytes)}` : "Leave empty for no quota."}`} error={error}>
        <Input
          id="quota-mb"
          inputMode="numeric"
          autoComplete="off"
          maxLength={9}
          value={value}
          aria-invalid={!!error}
          onChange={(e) => {
            setValue(e.target.value.trim())
            setError("")
          }}
        />
      </Field>
    </FormDialog>
  )
}

export function MailboxesTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const session = useSession().data
  const canWrite = hasPerm(session, orgId, "mailboxes:write")
  const canSettings = hasPerm(session, orgId, "mailboxes:settings")
  const canImport = hasPerm(session, orgId, "mailboxes:import")
  const canViewDelegates = canReadDelegates(session, orgId)
  const canManageDelegates = hasPerm(session, orgId, "mailboxes:delegates")
  const canViewAppPasswords = hasPerm(session, orgId, "mailboxes:app_passwords", "mailboxes:read")
  const canRevokeAppPasswords = hasPerm(session, orgId, "mailboxes:app_passwords")
  const canAudit = hasPerm(session, orgId, "audit:read")
  const mine = useQuery(myMailboxesQuery).data
  const personal = (m: Mailbox) => m.ownerUserId === session?.user.id || !!mine?.some((x) => x.id === m.id)
  const exportLocked = (m: Mailbox) => platformOnly(session, orgId) && !personal(m)
  const settingsLocked = (m: Mailbox) => platformOnly(session, orgId, "domain_admin", "helpdesk", "custom") && !personal(m)
  const how = grantHint(session, useQuery({ ...membersQuery(orgId), enabled: platformOnly(session, orgId) }).data)

  const mailboxes = useQuery({ ...mailboxesQuery(orgId), select: (all) => all.filter((m) => m.kind !== "resource") })
  const domains = useQuery(domainsQuery(orgId))
  const limits = useQuery(limitsQuery(orgId))
  const features = useQuery(featuresQuery(orgId)).data?.features
  const warnings = useQuery({ queryKey: ["quota-warnings", orgId], queryFn: () => api<QuotaWarnings>(`/organizations/${orgId}/quota-warnings`) })
  const [editingWarnings, setEditingWarnings] = useState(false)
  const [resizing, setResizing] = useState<Mailbox | null>(null)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Mailbox | null>(null)
  const [importingCsv, setImportingCsv] = useState(false)
  const [deleting, setDeleting] = useState<Mailbox | null>(null)
  const [delegating, setDelegating] = useState<Mailbox | null>(null)
  const [appPasswordsFor, setAppPasswordsFor] = useState<Mailbox | null>(null)
  const [managing, setManaging] = useState<Mailbox | null>(null)
  const [auditing, setAuditing] = useState<Mailbox | null>(null)
  const [converting, setConverting] = useState<Mailbox | null>(null)
  const [recovering, setRecovering] = useState<Mailbox | null>(null)
  const [restricting, setRestricting] = useState<Mailbox | null>(null)
  const [editingDefaults, setEditingDefaults] = useState(false)
  const org = useQuery({ ...organizationsQuery, select: (list) => list.find((o) => o.id === orgId) })
  const [exporting, setExporting] = useState<Mailbox | null>(null)
  const [grace, setGrace] = useState("30")
  const fileInput = useRef<HTMLInputElement>(null)
  const importTarget = useRef<{ mailbox: Mailbox; kind: ImportKind } | null>(null)

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { kind: "user", domainId: "", localPart: "", displayName: "", password: "", quotaMb: "" },
  })
  const editForm = useForm<EditValues>({
    resolver: zodResolver(editSchema),
    defaultValues: { displayName: "", quotaMb: "" },
  })
  const errors = form.formState.errors
  const [kind, domainId] = useWatch({ control: form.control, name: ["kind", "domainId"] })
  const defaults = useQuery({ ...domainDefaultsQuery(domainId), enabled: !!domainId })

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["mailboxes", orgId] })
    qc.invalidateQueries({ queryKey: ["my-mailboxes"] })
    qc.invalidateQueries({ queryKey: ["members", orgId] })
    qc.invalidateQueries({ queryKey: ["limits", orgId] })
  }

  async function quotaError(error: ApiError, current?: number, defaultBytes = 0) {
    if (error.code === "quota_required") return "Enter a quota. Every mailbox needs one while this organization has a storage limit."
    const { data } = await limits.refetch()
    if (!data) return error.message
    return `${defaultBytes ? `The default quota, ${formatBytes(defaultBytes)}, does not fit` : "That does not fit"}. ${storageRoom(data, current)}`
  }

  const create = useMutation({
    mutationFn: ({ quotaMb, password, displayName, ...rest }: Values) =>
      api<Mailbox>(`/organizations/${orgId}/mailboxes`, {
        method: "POST",
        body: {
          ...rest,
          displayName: displayName || undefined,
          password: password || undefined,
          quotaBytes: quotaMb ? Number(quotaMb) * MB : undefined,
        },
      }),
    meta: { silent: true },
    onSuccess: (m) => {
      toast.success(`Created ${m.address}`)
      refresh()
      setCreating(false)
      form.reset()
    },
    onError: async (error, v) => {
      const limit = limitMessage(error)
      if (limit) form.setError("root", { message: limit })
      else if (error instanceof ApiError && storageErrors.includes(error.code)) {
        form.setError("quotaMb", { message: await quotaError(error, undefined, v.quotaMb ? 0 : defaults.data?.effectiveQuotaBytes) })
      } else toast.error(errorMessage(error))
    },
  })
  const update = useMutation({
    mutationFn: ({ mailbox, values }: { mailbox: Mailbox; values: EditValues }) =>
      api<Mailbox>(`/mailboxes/${mailbox.id}`, {
        method: "PATCH",
        body: {
          displayName: values.displayName,
          quotaBytes: values.quotaMb ? Number(values.quotaMb) * MB : 0,
        },
      }),
    onSuccess: (m) => {
      toast.success(`Saved ${m.address}`)
      refresh()
      setEditing(null)
    },
  })
  const toggle = useMutation({
    mutationFn: (m: Mailbox) => api<Mailbox>(`/mailboxes/${m.id}`, { method: "PATCH", body: { active: !m.active } }),
    onSuccess: refresh,
  })
  const hide = useMutation({
    mutationFn: (m: Mailbox) => api<Mailbox>(`/mailboxes/${m.id}`, { method: "PATCH", body: { hiddenFromDirectory: !m.hiddenFromDirectory } }),
    onSuccess: (m) => {
      toast.success(m.hiddenFromDirectory ? `${m.address} is hidden from the directory` : `${m.address} is listed in the directory`)
      refresh()
    },
  })
  const importFile = useMutation({
    mutationFn: ({ mailbox, kind, file }: { mailbox: Mailbox; kind: ImportKind; file: File }) =>
      api<ImportCounts>(`/mailboxes/${mailbox.id}/${imports[kind].path}`, { method: "POST", body: file }),
    onSuccess: (r, { mailbox, kind }) =>
      toast.success(`Imported ${plural(r.imported + r.updated, imports[kind].noun)} for ${mailbox.address}`, {
        description: `${r.imported} new, ${r.updated} replaced, ${r.skipped} unreadable and skipped.`,
      }),
  })
  const remove = useMutation({
    mutationFn: (m: Mailbox) => api<Mailbox | undefined>(`/mailboxes/${m.id}`, { method: "DELETE", query: m.onHold ? undefined : { graceDays: Number(grace) } }),
    onSuccess: (_saved, m) => {
      if (m.onHold) toast.success(`${m.address} is closed`, { description: "It is on hold, so its mail stays in place." })
      else if (Number(grace) > 0) toast.success(`${m.address} will be deleted`, { description: "Its mail is removed when the grace period ends. Restore it from this list until then." })
      else toast.success(`${m.address} was deleted`, { description: "Its mail is being removed from disk." })
      setDeleting(null)
      refresh()
    },
  })
  const saveProtocols = useMutation({
    mutationFn: ({ m, protocols }: { m: Mailbox; protocols: Protocol[] }) => api<Mailbox>(`/mailboxes/${m.id}`, { method: "PATCH", body: { protocols } }),
    onSuccess: (m) => {
      toast.success(`Saved protocol access for ${m.address}`)
      setRestricting(null)
      refresh()
    },
  })
  const saveDefaults = useMutation({
    mutationFn: (mailboxProtocols: Protocol[]) => api<Organization>(`/organizations/${orgId}`, { method: "PATCH", body: { mailboxProtocols } }),
    onSuccess: () => {
      toast.success("Saved protocol access for new mailboxes")
      setEditingDefaults(false)
      qc.invalidateQueries({ queryKey: organizationsQuery.queryKey })
    },
  })
  const restore = useMutation({
    mutationFn: (m: Mailbox) => api<Mailbox>(`/mailboxes/${m.id}/restore`, { method: "POST" }),
    onSuccess: (m) => {
      toast.success(`${m.address} was restored`)
      refresh()
    },
  })
  const hold = useMutation({
    mutationFn: (m: Mailbox) => api<Mailbox>(`/mailboxes/${m.id}`, { method: "PATCH", body: { onHold: !m.onHold } }),
    onSuccess: (m) => {
      toast.success(m.onHold ? `${m.address} is on hold` : `Hold released for ${m.address}`)
      refresh()
    },
  })
  const convert = useMutation({
    mutationFn: (m: Mailbox) => api<Mailbox>(`/mailboxes/${m.id}/convert`, { method: "POST", body: { kind: m.kind === "shared" ? "user" : "shared" } }),
    onSuccess: (m) => {
      toast.success(`${m.address} is now a ${m.kind} mailbox`)
      setConverting(null)
      refresh()
    },
  })
  const refreshSizes = useMutation({
    mutationFn: () => api<{ updated: number }>(`/organizations/${orgId}/reports/refresh`, { method: "POST" }),
    onSuccess: (r) => {
      toast.success(`Updated sizes for ${plural(r.updated, "mailbox")}`)
      qc.invalidateQueries({ queryKey: ["mailboxes", orgId] })
      qc.invalidateQueries({ queryKey: ["reports", orgId] })
    },
  })

  function pick(mailbox: Mailbox, kind: ImportKind) {
    importTarget.current = { mailbox, kind }
    if (!fileInput.current) return
    fileInput.current.accept = imports[kind].accept
    fileInput.current.click()
  }

  function openEdit(m: Mailbox) {
    editForm.reset({
      displayName: m.displayName,
      quotaMb: m.quotaBytes ? String(Math.round(m.quotaBytes / MB)) : "",
    })
    setEditing(m)
  }

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Mailboxes"
        description="User mailboxes have an owner account that signs in with the address. Shared mailboxes have members instead."
        actions={
          <>
            <Button
              variant="secondary"
              disabled={!mailboxes.data?.length}
              onClick={() => downloadCsv("mailboxes.csv", exportMailboxesCsv(mailboxes.data ?? []))}
            >
              <Download aria-hidden />
              Export CSV
            </Button>
            {canWrite && (
              <Button variant="secondary" onClick={() => setImportingCsv(true)}>
                <Upload aria-hidden />
                Import CSV
              </Button>
            )}
            {canSettings && (
              <Button variant="secondary" onClick={() => refreshSizes.mutate()} loading={refreshSizes.isPending}>
                <RefreshCw aria-hidden />
                Refresh sizes
              </Button>
            )}
            {canSettings && (
              <Button
                variant="secondary"
                loading={editingWarnings && warnings.isPending}
                onClick={() => {
                  if (warnings.isError) {
                    toast.error(errorMessage(warnings.error))
                    warnings.refetch()
                  } else setEditingWarnings(true)
                }}
              >
                <BellRing aria-hidden />
                Storage warnings
              </Button>
            )}
            {canWrite && (
              <Button variant="secondary" disabled={!org.data} onClick={() => setEditingDefaults(true)}>
                <Plug aria-hidden />
                Protocols for new mailboxes
              </Button>
            )}
            {canWrite && (
              <Button onClick={() => setCreating(true)}>
                <Plus aria-hidden />
                Create mailbox
              </Button>
            )}
          </>
        }
      />
      <QueryView query={mailboxes} empty={<EmptyState icon={Inbox} title="No mailboxes" description={canWrite ? "Create a mailbox on one of this organization's domains." : "No mailboxes are visible for your scope."} />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Display name</TableHead>
                <TableHead>Storage</TableHead>
                <TableHead>Last sign-in</TableHead>
                <TableHead className="text-right">Mail (30 days)</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="w-36">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((m) => (
                <TableRow key={m.id}>
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <span className="font-mono text-sm">{m.address}</span>
                      {m.hiddenFromDirectory && <StatusBadge tone="neutral">Hidden</StatusBadge>}
                    </div>
                  </TableCell>
                  <TableCell>{m.kind === "shared" ? <StatusBadge tone="neutral">Shared</StatusBadge> : "User"}</TableCell>
                  <TableCell>{m.displayName || "—"}</TableCell>
                  <TableCell>
                    <MailboxStorage mailbox={m} levels={warnings.data?.percents} />
                  </TableCell>
                  <TableCell>
                    <LastSignIn
                      kind={m.kind}
                      lastSignInAt={m.lastSignInAt}
                      lastWebLoginAt={m.lastWebLoginAt}
                      lastImapLoginAt={m.lastImapLoginAt}
                      lastSmtpLoginAt={m.lastSmtpLoginAt}
                    />
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    <div className="flex flex-col items-end gap-0.5">
                      <span>{(m.sent30d ?? 0).toLocaleString()} sent</span>
                      <span className="text-xs text-fg-tertiary">{(m.received30d ?? 0).toLocaleString()} received</span>
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap items-center gap-3">
                      {canSettings && (
                        <Switch
                          checked={m.active}
                          disabled={toggle.isPending && toggle.variables?.id === m.id}
                          onCheckedChange={() => toggle.mutate(m)}
                          aria-label={`${m.active ? "Deactivate" : "Activate"} ${m.address}`}
                        />
                      )}
                      {m.retainedAt ? (
                        <StatusBadge tone="warning">Retained</StatusBadge>
                      ) : (
                        <StatusBadge tone={m.active ? "success" : "neutral"}>{m.active ? "Active" : "Inactive"}</StatusBadge>
                      )}
                      {m.onHold && <StatusBadge tone="warning">On hold</StatusBadge>}
                      {m.purgeAfter && <StatusBadge tone="danger">Deletes {formatDateTime(m.purgeAfter)}</StatusBadge>}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1">
                      {canViewDelegates && (
                        <IconButton label={m.kind === "shared" ? `Members of ${m.address}` : `Delegates for ${m.address}`} size="icon-sm" onClick={() => setDelegating(m)}>
                          <Users />
                        </IconButton>
                      )}
                      {canViewAppPasswords && (
                        <IconButton label={`App passwords for ${m.address}`} size="icon-sm" onClick={() => setAppPasswordsFor(m)}>
                          <KeyRound />
                        </IconButton>
                      )}
                      {canWrite && m.purgeAfter && (
                        <IconButton label={`Restore ${m.address}`} size="icon-sm" disabled={restore.isPending} onClick={() => restore.mutate(m)}>
                          <RotateCcw />
                        </IconButton>
                      )}
                      {canWrite && (
                        <IconButton label={`Delete ${m.address}`} size="icon-sm" onClick={() => { setGrace("30"); setDeleting(m) }}>
                          <Trash2 />
                        </IconButton>
                      )}
                      {(canSettings || canWrite || canImport || canAudit) && (
                      <DropdownMenu>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <DropdownMenuTrigger asChild>
                                <Button variant="quiet" size="icon-sm" aria-label={`More actions for ${m.address}`} loading={(importFile.isPending && importFile.variables?.mailbox.id === m.id) || (hide.isPending && hide.variables?.id === m.id)}>
                                  <Ellipsis />
                                </Button>
                              </DropdownMenuTrigger>
                            </TooltipTrigger>
                            <TooltipContent>{`More actions for ${m.address}`}</TooltipContent>
                          </Tooltip>
                          <DropdownMenuContent align="end" className="w-auto min-w-64">
                            {exportLocked(m) && (
                              <>
                                <LockNote className="max-w-80 px-3 py-2">Dimmed actions need the owner or administrator role. {how}</LockNote>
                                <DropdownMenuSeparator />
                              </>
                            )}
                            {canSettings && (
                              <DropdownMenuItem onSelect={() => openEdit(m)}>
                                <Pencil aria-hidden />
                                Edit settings
                              </DropdownMenuItem>
                            )}
                            {canWrite && (
                              <DropdownMenuItem disabled={exportLocked(m)} onSelect={() => setExporting(m)}>
                                <Download aria-hidden />
                                Export mailbox
                              </DropdownMenuItem>
                            )}
                            {canWrite && (
                              <DropdownMenuItem onSelect={() => hold.mutate(m)}>
                                <Shield aria-hidden />
                                {m.onHold ? "Release hold" : "Place on hold"}
                              </DropdownMenuItem>
                            )}
                            {canSettings && (
                              <DropdownMenuItem disabled={settingsLocked(m)} onSelect={() => setRecovering(m)}>
                                <ArchiveRestore aria-hidden />
                                Recover deleted items…
                              </DropdownMenuItem>
                            )}
                            {canSettings && (
                              <DropdownMenuItem onSelect={() => setRestricting(m)}>
                                <Plug aria-hidden />
                                Protocol access…
                              </DropdownMenuItem>
                            )}
                            {canAudit && (
                              <DropdownMenuItem onSelect={() => setAuditing(m)}>
                                <ShieldCheck aria-hidden />
                                Access report
                              </DropdownMenuItem>
                            )}
                            {canSettings && (
                              <DropdownMenuItem onSelect={() => setResizing(m)}>
                                <HardDrive aria-hidden />
                                Change quota…
                              </DropdownMenuItem>
                            )}
                            {canWrite && (
                              <DropdownMenuItem onSelect={() => hide.mutate(m)}>
                                {m.hiddenFromDirectory ? <Eye aria-hidden /> : <EyeOff aria-hidden />}
                                {m.hiddenFromDirectory ? "Show in directory" : "Hide from directory"}
                              </DropdownMenuItem>
                            )}
                            {m.active && (
                              <>
                                {canSettings && (
                                  <DropdownMenuItem onSelect={() => setManaging(m)}>
                                    <ListFilter aria-hidden />
                                    Mail rules and automatic replies
                                  </DropdownMenuItem>
                                )}
                                {canImport && features?.calendar !== false && (
                                  <DropdownMenuItem onSelect={() => pick(m, "calendar")}>
                                    <CalendarArrowUp aria-hidden />
                                    Import calendar (.ics)
                                  </DropdownMenuItem>
                                )}
                                {canImport && features?.calendar !== false && (
                                  <DropdownMenuItem onSelect={() => pick(m, "contacts")}>
                                    <Upload aria-hidden />
                                    Import contacts (.vcf)
                                  </DropdownMenuItem>
                                )}
                                {canWrite && <DropdownMenuSeparator />}
                                {canWrite && (
                                  <DropdownMenuItem onSelect={() => setConverting(m)}>
                                    <ArrowRightLeft aria-hidden />
                                    {m.kind === "shared" ? "Convert to user mailbox…" : "Convert to shared mailbox…"}
                                  </DropdownMenuItem>
                                )}
                              </>
                            )}
                          </DropdownMenuContent>
                        </DropdownMenu>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <RetentionSetting orgId={orgId} />

      <FormDialog
        open={creating}
        onOpenChange={(open) => {
          setCreating(open)
          if (!open) form.reset()
        }}
        title="Create mailbox"
        description={kind === "shared" ? "A shared mailbox has no sign-in. Add members after creating it." : "Aether creates or links an owner account with the mailbox address."}
        submitLabel="Create mailbox"
        pending={create.isPending}
        error={errors.root?.message}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
      >
        <Controller
          control={form.control}
          name="kind"
          render={({ field }) => (
            <fieldset className="flex flex-col items-start gap-2">
              <legend className="mb-2 text-label text-fg-secondary">Type</legend>
              <SegmentedControl
                label="Type"
                options={kinds}
                value={field.value}
                onChange={(value) => {
                  field.onChange(value)
                  if (value === "shared") form.setValue("password", "")
                }}
              />
            </fieldset>
          )}
        />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="mb-local" label="Local part" error={errors.localPart?.message}>
            <Input id="mb-local" placeholder="jane" autoComplete="off" spellCheck={false} aria-invalid={!!errors.localPart} {...form.register("localPart")} />
          </Field>
          <DomainSelect id="mb-domain" orgId={orgId} control={form.control} />
        </div>
        <Field id="mb-name" label="Display name" hint="Shown as the sender name. Optional.">
          <Input id="mb-name" autoComplete="off" {...form.register("displayName")} />
        </Field>
        {kind !== "shared" && (
          <Field id="mb-password" label="Password" hint="Optional web sign-in password for the owner, at least 12 characters, or empty for single sign-on." error={errors.password?.message}>
            <Input id="mb-password" type="password" autoComplete="new-password" aria-invalid={!!errors.password} {...form.register("password")} />
          </Field>
        )}
        <Field id="mb-quota" label="Quota in MB" hint={quotaHint(limits.data, defaults.data)} error={errors.quotaMb?.message}>
          <Input id="mb-quota" inputMode="numeric" autoComplete="off" aria-invalid={!!errors.quotaMb} {...form.register("quotaMb")} />
        </Field>
      </FormDialog>

      <FormDialog
        open={!!editing}
        onOpenChange={(open) => {
          if (!open) setEditing(null)
        }}
        title="Edit mailbox settings"
        description={editing ? <span className="font-mono">{editing.address}</span> : undefined}
        submitLabel="Save settings"
        pending={update.isPending}
        onSubmit={editForm.handleSubmit((v) => editing && update.mutate({ mailbox: editing, values: v }))}
      >
        <Field id="edit-mb-name" label="Display name" hint="Shown as the sender name.">
          <Input id="edit-mb-name" autoComplete="off" {...editForm.register("displayName")} />
        </Field>
        <Field id="edit-mb-quota" label="Quota in MB" hint="Leave empty for no limit." error={editForm.formState.errors.quotaMb?.message}>
          <Input id="edit-mb-quota" inputMode="numeric" autoComplete="off" aria-invalid={!!editForm.formState.errors.quotaMb} {...editForm.register("quotaMb")} />
        </Field>
      </FormDialog>

      <DelegatesDialog mailbox={delegating} canManage={canManageDelegates} how={how} onOpenChange={(open) => !open && setDelegating(null)} />
      <AppPasswordsDialog mailbox={appPasswordsFor} canRevoke={canRevokeAppPasswords} onOpenChange={(open) => !open && setAppPasswordsFor(null)} />
      {editingWarnings && warnings.data && <QuotaWarningsDialog orgId={orgId} warnings={warnings.data} onClose={() => setEditingWarnings(false)} />}

      <RecoverDialog mailbox={recovering} onOpenChange={(open) => !open && setRecovering(null)} />

      <ProtocolsDialog
        key={restricting?.id ?? "mailbox"}
        initial={restricting?.protocols ?? null}
        title={`Protocol access for ${restricting?.address}`}
        description="Turning off IMAP or ManageSieve ends open sessions right away. App passwords stay and work again when you turn a protocol back on."
        pending={saveProtocols.isPending}
        onOpenChange={(open) => !open && setRestricting(null)}
        onSave={(protocols) => restricting && saveProtocols.mutate({ m: restricting, protocols })}
      />

      <ProtocolsDialog
        key={editingDefaults ? "defaults-open" : "defaults"}
        initial={editingDefaults ? (org.data?.mailboxProtocols ?? null) : null}
        title="Protocols for new mailboxes"
        description="Mailboxes created from now on, including at single sign-on, start with these protocols. Existing mailboxes keep theirs."
        pending={saveDefaults.isPending}
        onOpenChange={setEditingDefaults}
        onSave={(protocols) => saveDefaults.mutate(protocols)}
      />

      {resizing && <QuotaDialog key={resizing.id} mailbox={resizing} limits={limits.data} quotaError={quotaError} onSaved={refresh} onClose={() => setResizing(null)} />}

      <input
        ref={fileInput}
        type="file"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0]
          if (file && importTarget.current) importFile.mutate({ ...importTarget.current, file })
          e.target.value = ""
        }}
      />

      <Sheet open={!!managing} onOpenChange={(open) => !open && setManaging(null)}>
        <SheetContent side="right" className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[800px]">
          <SheetHeader className="border-b border-border p-6">
            <SheetTitle>Mail rules and automatic replies</SheetTitle>
            <SheetDescription>{managing?.address}</SheetDescription>
          </SheetHeader>
          <div className="flex flex-1 flex-col gap-6 overflow-y-auto p-6">{managing && <MailboxRules key={managing.id} mailboxId={managing.id} outsideForwardingOff={features?.externalForwarding === false} forwardingLocked={settingsLocked(managing)} grantHint={how} />}</div>
        </SheetContent>
      </Sheet>

      <Sheet open={!!auditing} onOpenChange={(open) => !open && setAuditing(null)}>
        <SheetContent side="right" className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[960px]">
          <SheetHeader className="border-b border-border p-6">
            <SheetTitle>Access report</SheetTitle>
            <SheetDescription>{auditing ? `Who other than the owner accessed ${auditing.address}.` : ""}</SheetDescription>
          </SheetHeader>
          <div className="flex flex-1 flex-col gap-6 overflow-y-auto p-6">{auditing && <MailboxAccessReport key={auditing.id} mailboxId={auditing.id} />}</div>
        </SheetContent>
      </Sheet>

      <ExportMailboxDialog mailbox={exporting} onOpenChange={(open) => !open && setExporting(null)} />

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={deleting?.onHold ? `Close ${deleting.address}?` : `Delete ${deleting?.address}?`}
        description={
          deleting?.onHold
            ? "This mailbox is on hold. It stops receiving mail and its app passwords stop working, and its mail stays on disk until the hold is released."
            : "The mailbox stops receiving mail immediately and its app passwords stop working. Stored mail is removed when the grace period ends, and can be restored until then."
        }
        confirmLabel={deleting?.onHold ? "Close mailbox" : Number(grace) > 0 ? "Schedule deletion" : "Delete mailbox"}
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      >
        {!deleting?.onHold && (
          <Field id="delete-grace" label="When to remove the stored mail">
            <Select value={grace} onValueChange={setGrace}>
              <SelectTrigger id="delete-grace" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="30">After 30 days</SelectItem>
                <SelectItem value="7">After 7 days</SelectItem>
                <SelectItem value="0">Now</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        )}
      </ConfirmDialog>

      <ConfirmDialog
        open={!!converting}
        onOpenChange={(open) => !open && setConverting(null)}
        title={`Convert ${converting?.address} to a ${converting?.kind === "shared" ? "user" : "shared"} mailbox?`}
        description={
          converting?.kind === "shared"
            ? "If the address had an owner account before, it is restored as the owner. Members keep their access as delegates but lose the shared folders in their mail apps."
            : "The owner account stops signing in and its sessions end. Every app password for this mailbox stops working. Mail stays in place and members you add can open it."
        }
        confirmLabel={`Convert to ${converting?.kind === "shared" ? "user" : "shared"} mailbox`}
        pending={convert.isPending}
        onConfirm={() => converting && convert.mutate(converting)}
      />
      <CsvImportDialog
        orgId={orgId}
        kind="mailbox"
        open={importingCsv}
        onOpenChange={setImportingCsv}
        defaultDomain={domains.data?.[0]?.name}
        onCompleted={refresh}
      />
    </section>
  )
}

"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowRightLeft, CalendarArrowUp, Ellipsis, Inbox, ListFilter, Plus, Trash2, Upload, Users } from "lucide-react"
import { useRef, useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { DomainSelect } from "@/components/admin/domain-select"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { MailboxRules } from "@/components/settings/mail-rules"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { api, ApiError, errorMessage } from "@/lib/api"
import { formatBytes, plural } from "@/lib/format"
import type { Delegate, DelegateAccess, ImportCounts, Mailbox } from "@/lib/types"

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
      .regex(/^[a-z0-9]([a-z0-9._+-]*[a-z0-9])?$/, "Use letters, digits, dots, hyphens, plus signs or underscores."),
    displayName: z.string().trim(),
    password: z.union([z.literal(""), z.string().min(12, "Use at least 12 characters, or leave it empty.")]),
    quotaMb: z.string().regex(/^\d*$/, "Enter a whole number of megabytes."),
  })
  .refine((v) => v.kind === "user" || v.password === "", { message: "Shared mailboxes have no password.", path: ["password"] })

type Values = z.infer<typeof schema>

const kinds = [
  { value: "user" as const, label: "User mailbox" },
  { value: "shared" as const, label: "Shared mailbox" },
]

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

function DelegatesDialog({ mailbox, onOpenChange }: { mailbox: Mailbox | null; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient()
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

  return (
    <>
      <FormDialog
        open={!!mailbox}
        onOpenChange={(open) => {
          onOpenChange(open)
          if (!open) form.reset()
        }}
        title={shared ? `Members of ${mailbox?.address}` : `Delegates for ${mailbox?.address}`}
        description={
          shared
            ? "Full access lets a member read, organize and file mail. Send as and Send on behalf are separate: Send as sends from this address; Send on behalf shows the member's name as the sender."
            : "Full access lets a delegate read and organize this mailbox; Send as additionally allows sending from the address and creating app passwords for it. Read only allows reading in Aether Mail and nothing else."
        }
        submitLabel={shared ? "Add member" : "Add delegate"}
        cancelLabel="Done"
        pending={add.isPending}
        onSubmit={form.handleSubmit((v) => add.mutate(v))}
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
                      <Select value={shown.access} disabled={change.isPending} onValueChange={(access) => change.mutate({ ...rights(d), access: access as DelegateAccess })}>
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
                          disabled={change.isPending}
                          onCheckedChange={(sendAs) => change.mutate({ ...rights(d), sendAs })}
                          aria-label={`Send as for ${d.email}`}
                        />
                        <Label htmlFor={`send-as-${d.userId}`}>Send as</Label>
                      </div>
                      <div className="flex items-center gap-2">
                        <Switch
                          id={`send-on-behalf-${d.userId}`}
                          checked={shown.sendOnBehalf}
                          disabled={change.isPending}
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
        <Field id="delegate-email" label="Email" hint="The email of an active person in this organization." error={form.formState.errors.email?.message}>
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

export function MailboxesTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const mailboxes = useQuery({ queryKey: ["mailboxes", orgId], queryFn: () => api<Mailbox[]>(`/organizations/${orgId}/mailboxes`) })
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Mailbox | null>(null)
  const [delegating, setDelegating] = useState<Mailbox | null>(null)
  const [managing, setManaging] = useState<Mailbox | null>(null)
  const [converting, setConverting] = useState<Mailbox | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const importTarget = useRef<{ mailbox: Mailbox; kind: ImportKind } | null>(null)
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { kind: "user", domainId: "", localPart: "", displayName: "", password: "", quotaMb: "" },
  })
  const errors = form.formState.errors
  const kind = useWatch({ control: form.control, name: "kind" })

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["mailboxes", orgId] })
    qc.invalidateQueries({ queryKey: ["my-mailboxes"] })
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
    onSuccess: (m) => {
      toast.success(`Created ${m.address}`)
      refresh()
      setCreating(false)
      form.reset()
    },
  })
  const toggle = useMutation({
    mutationFn: (m: Mailbox) => api<Mailbox>(`/mailboxes/${m.id}`, { method: "PATCH", body: { active: !m.active } }),
    onSuccess: refresh,
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
    mutationFn: (id: string) => api(`/mailboxes/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Mailbox deleted")
      setDeleting(null)
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

  function pick(mailbox: Mailbox, kind: ImportKind) {
    importTarget.current = { mailbox, kind }
    if (!fileInput.current) return
    fileInput.current.accept = imports[kind].accept
    fileInput.current.click()
  }

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Mailboxes"
        description="User mailboxes have an owner account that signs in with the address. Shared mailboxes have members instead."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            Create mailbox
          </Button>
        }
      />
      <QueryView query={mailboxes} empty={<EmptyState icon={Inbox} title="No mailboxes" description="Create a mailbox on one of this organization's domains." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Display name</TableHead>
                <TableHead>Quota</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="w-28">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((m) => (
                <TableRow key={m.id}>
                  <TableCell className="font-mono text-sm">{m.address}</TableCell>
                  <TableCell>{m.kind === "shared" ? <StatusBadge tone="neutral">Shared</StatusBadge> : "User"}</TableCell>
                  <TableCell>{m.displayName || "—"}</TableCell>
                  <TableCell>{m.quotaBytes ? formatBytes(m.quotaBytes) : "No limit"}</TableCell>
                  <TableCell>
                    <div className="flex items-center gap-3">
                      <Switch
                        checked={m.active}
                        disabled={toggle.isPending && toggle.variables?.id === m.id}
                        onCheckedChange={() => toggle.mutate(m)}
                        aria-label={`${m.active ? "Deactivate" : "Activate"} ${m.address}`}
                      />
                      <StatusBadge tone={m.active ? "success" : "neutral"}>{m.active ? "Active" : "Inactive"}</StatusBadge>
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1">
                      <IconButton label={m.kind === "shared" ? `Members of ${m.address}` : `Delegates for ${m.address}`} size="icon-sm" onClick={() => setDelegating(m)}>
                        <Users />
                      </IconButton>
                      <IconButton label={`Delete ${m.address}`} size="icon-sm" onClick={() => setDeleting(m)}>
                        <Trash2 />
                      </IconButton>
                      {m.active && (
                        <DropdownMenu>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <DropdownMenuTrigger asChild>
                                <Button variant="quiet" size="icon-sm" aria-label={`More actions for ${m.address}`} loading={importFile.isPending && importFile.variables?.mailbox.id === m.id}>
                                  <Ellipsis />
                                </Button>
                              </DropdownMenuTrigger>
                            </TooltipTrigger>
                            <TooltipContent>{`More actions for ${m.address}`}</TooltipContent>
                          </Tooltip>
                          <DropdownMenuContent align="end" className="w-auto min-w-64">
                            <DropdownMenuItem onSelect={() => setManaging(m)}>
                              <ListFilter aria-hidden />
                              Mail rules and automatic replies
                            </DropdownMenuItem>
                            <DropdownMenuItem onSelect={() => pick(m, "calendar")}>
                              <CalendarArrowUp aria-hidden />
                              Import calendar (.ics)
                            </DropdownMenuItem>
                            <DropdownMenuItem onSelect={() => pick(m, "contacts")}>
                              <Upload aria-hidden />
                              Import contacts (.vcf)
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem onSelect={() => setConverting(m)}>
                              <ArrowRightLeft aria-hidden />
                              {m.kind === "shared" ? "Convert to user mailbox…" : "Convert to shared mailbox…"}
                            </DropdownMenuItem>
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
        <Field id="mb-quota" label="Quota in MB" hint="Leave empty for no limit." error={errors.quotaMb?.message}>
          <Input id="mb-quota" inputMode="numeric" autoComplete="off" aria-invalid={!!errors.quotaMb} {...form.register("quotaMb")} />
        </Field>
      </FormDialog>

      <DelegatesDialog mailbox={delegating} onOpenChange={(open) => !open && setDelegating(null)} />

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
          <div className="flex flex-1 flex-col gap-6 overflow-y-auto p-6">{managing && <MailboxRules key={managing.id} mailboxId={managing.id} />}</div>
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Delete ${deleting?.address}?`}
        description="The mailbox stops receiving mail immediately and its app passwords stop working. Stored mail stays on disk until an operator removes it."
        confirmLabel="Delete mailbox"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />

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
    </section>
  )
}

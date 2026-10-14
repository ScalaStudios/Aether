"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { KeyRound, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyButton } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { PageContainer } from "@/components/page-container"
import { PageHeader, SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { DialogClose } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { myMailboxesQuery } from "@/lib/queries"
import type { AppPassword, CreatedAppPassword } from "@/lib/types"

export function SettingsView() {
  const session = useSession().data

  return (
    <PageContainer>
      <PageHeader title="Settings" description="Your account details and the app passwords that let mail clients connect to your mailboxes." />
      {session && (
        <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
          <SectionHeader title="Profile" description="Managed by your administrator or identity provider." />
          <dl className="grid gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
              <dt className="text-label text-fg-tertiary">Name</dt>
              <dd>{session.user.displayName || "Not set"}</dd>
            </div>
            <div className="flex flex-col gap-1">
              <dt className="text-label text-fg-tertiary">Email</dt>
              <dd className="break-all">{session.user.email}</dd>
            </div>
            <div className="flex flex-col gap-1">
              <dt className="text-label text-fg-tertiary">Account</dt>
              <dd className="flex flex-wrap gap-2">
                <StatusBadge tone={session.user.status === "active" ? "success" : "danger"}>
                  {session.user.status === "active" ? "Active" : session.user.status === "suspended" ? "Suspended" : "Deprovisioned"}
                </StatusBadge>
                {session.user.isPlatformAdmin && <StatusBadge tone="info">Platform administrator</StatusBadge>}
              </dd>
            </div>
            <div className="flex flex-col gap-1">
              <dt className="text-label text-fg-tertiary">Organizations</dt>
              <dd>
                {session.memberships.length
                  ? session.memberships.map((m) => `${m.orgName} (${m.role})`).join(", ")
                  : "None"}
              </dd>
            </div>
          </dl>
        </section>
      )}
      <AppPasswords />
    </PageContainer>
  )
}

const createSchema = z.object({
  name: z.string().trim().min(1, "Name the device or client, for example “Laptop Thunderbird”."),
  mailboxId: z.string().min(1, "Choose a mailbox."),
})

type CreateValues = z.infer<typeof createSchema>

function AppPasswords() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ["app-passwords"], queryFn: () => api<AppPassword[]>("/me/app-passwords") })
  const mailboxes = useQuery(myMailboxesQuery)
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreatedAppPassword | null>(null)
  const [revoking, setRevoking] = useState<AppPassword | null>(null)
  const form = useForm<CreateValues>({ resolver: zodResolver(createSchema), defaultValues: { name: "", mailboxId: "" } })

  const create = useMutation({
    mutationFn: (values: CreateValues) => api<CreatedAppPassword>("/me/app-passwords", { method: "POST", body: values }),
    onSuccess: (data) => {
      setCreated(data)
      form.reset()
      qc.invalidateQueries({ queryKey: ["app-passwords"] })
    },
  })
  const revoke = useMutation({
    mutationFn: (id: string) => api(`/me/app-passwords/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("App password revoked")
      setRevoking(null)
      qc.invalidateQueries({ queryKey: ["app-passwords"] })
    },
  })

  function close(open: boolean) {
    setCreating(open)
    if (!open) {
      setCreated(null)
      form.reset()
    }
  }

  const host = typeof window === "undefined" ? "" : window.location.hostname

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="App passwords"
        description="Use an app password instead of your sign-in password in IMAP and SMTP mail clients. Each one works for a single mailbox."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus aria-hidden />
            Create app password
          </Button>
        }
      />
      <QueryView
        query={list}
        empty={<EmptyState icon={KeyRound} title="No app passwords" description="Create one to connect a desktop or phone mail client." />}
      >
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Mailbox</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((p) => (
                <TableRow key={p.id}>
                  <TableCell className="font-medium">{p.name}</TableCell>
                  <TableCell>{p.mailboxAddress}</TableCell>
                  <TableCell>{formatDate(p.createdAt)}</TableCell>
                  <TableCell>
                    <IconButton label={`Revoke ${p.name}`} size="icon-sm" onClick={() => setRevoking(p)}>
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
        title={created ? "App password created" : "Create app password"}
        description={created ? "Copy the password now. Aether shows it only once." : "The password is generated for you and shown once."}
        submitLabel="Create"
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
        footer={
          created && (
            <DialogClose asChild>
              <Button type="button">Done</Button>
            </DialogClose>
          )
        }
      >
        {created ? (
          <ClientSettings password={created.password} username={created.mailboxAddress} host={host} />
        ) : (
          <>
            <Field id="ap-name" label="Name" error={form.formState.errors.name?.message}>
              <Input id="ap-name" autoComplete="off" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
            </Field>
            <Controller
              control={form.control}
              name="mailboxId"
              render={({ field, fieldState }) => (
                <Field id="ap-mailbox" label="Mailbox" error={fieldState.error?.message}>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="ap-mailbox" aria-invalid={!!fieldState.error} className="w-full">
                      <SelectValue placeholder={mailboxes.isPending ? "Loading mailboxes…" : "Choose a mailbox"} />
                    </SelectTrigger>
                    <SelectContent>
                      {mailboxes.data?.map((m) => (
                        <SelectItem key={m.id} value={m.id}>
                          {m.address}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}
            />
          </>
        )}
      </FormDialog>

      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(open) => !open && setRevoking(null)}
        title="Revoke app password?"
        description={`Mail clients using “${revoking?.name}” stop connecting immediately. This cannot be undone.`}
        confirmLabel="Revoke"
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </section>
  )
}

function ClientSettings({ password, username, host }: { password: string; username: string; host: string }) {
  const rows: [string, string][] = [
    ["Username", username],
    ["IMAP server", `${host} · port 993 SSL/TLS or 143 STARTTLS`],
    ["SMTP server", `${host} · port 465 SSL/TLS or 587 STARTTLS`],
  ]

  return (
    <div className="flex flex-col gap-4">
      <Field id="ap-secret" label="Password">
        <div className="flex items-center gap-2">
          <Input id="ap-secret" readOnly value={password} className="font-mono" onFocus={(e) => e.currentTarget.select()} />
          <CopyButton value={password} label="Copy password" />
        </div>
      </Field>
      <dl className="flex flex-col rounded-xl border border-border">
        {rows.map(([label, value]) => (
          <div key={label} className="flex flex-col gap-1 border-b border-border p-4 last:border-b-0 sm:flex-row sm:gap-4">
            <dt className="w-32 shrink-0 text-label text-fg-tertiary">{label}</dt>
            <dd className="font-mono text-sm break-all">{value}</dd>
          </div>
        ))}
      </dl>
      <p className="text-sm text-fg-tertiary">Authentication: normal password. Use the full mailbox address as the username for both servers.</p>
    </div>
  )
}

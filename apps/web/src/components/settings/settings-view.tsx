"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { KeyRound, Plus, Smartphone, Trash2 } from "lucide-react"
import { useRef, useState, useSyncExternalStore } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { Avatar, refreshAvatars } from "@/components/avatar"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyButton, CopyField } from "@/components/copy-button"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { PageContainer } from "@/components/page-container"
import { ImportMail } from "@/components/settings/import-mail"
import { MailRules } from "@/components/settings/mail-rules"
import { Signatures } from "@/components/settings/signatures"
import { PageHeader, SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { DialogClose } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api, apiUrl, errorMessage } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { myMailboxesQuery } from "@/lib/queries"
import type { AppPassword, CreatedAppPassword } from "@/lib/types"

export function SettingsView() {
  const session = useSession().data

  return (
    <PageContainer>
      <PageHeader title="Settings" description="Your account details, mail rules, and the app passwords that let mail, calendar and contacts apps connect to your mailboxes." />
      {session && (
        <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
          <SectionHeader title="Profile" description="Your picture shows next to mail you send to people in your organization. If your sign-in provider has a picture, Aether uses it until you upload your own. Your name and email are managed by your administrator or identity provider." />
          <ProfilePicture email={session.user.email} name={session.user.displayName} />
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
      <Signatures />
      <MailRules />
      <ImportMail />
      <AppPasswords />
      <CalendarApps />
    </PageContainer>
  )
}

async function squareJpeg(file: File) {
  const bitmap = await createImageBitmap(file).catch(() => {
    throw new Error("That file isn't a picture that can be read. Choose a JPEG, PNG, or WebP image.")
  })
  const side = Math.min(bitmap.width, bitmap.height)
  const canvas = document.createElement("canvas")
  canvas.width = canvas.height = 256
  const ctx = canvas.getContext("2d")!
  ctx.fillStyle = "#fff"
  ctx.fillRect(0, 0, 256, 256)
  ctx.drawImage(bitmap, (bitmap.width - side) / 2, (bitmap.height - side) / 2, side, side, 0, 0, 256, 256)
  return new Promise<Blob>((resolve, reject) => canvas.toBlob((b) => (b ? resolve(b) : reject(new Error("The picture could not be prepared."))), "image/jpeg", 0.9))
}

function ProfilePicture({ email, name }: { email: string; name: string }) {
  const qc = useQueryClient()
  const fileInput = useRef<HTMLInputElement>(null)
  const hasPicture = useQuery({
    queryKey: ["my-avatar"],
    queryFn: async () => (await fetch(apiUrl(`/avatars/${encodeURIComponent(email.toLowerCase())}`, { v: Date.now() }))).ok,
  })
  const changed = () => {
    refreshAvatars()
    qc.invalidateQueries({ queryKey: ["my-avatar"] })
  }
  const upload = useMutation({
    mutationFn: async (file: File) => api("/me/avatar", { method: "PUT", body: await squareJpeg(file) }),
    onSuccess: () => {
      toast.success("Profile picture updated")
      changed()
    },
  })
  const remove = useMutation({
    mutationFn: () => api("/me/avatar", { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Profile picture removed")
      changed()
    },
  })

  return (
    <div className="flex items-center gap-4">
      <Avatar address={email} name={name} className="size-16 text-lg" />
      <input
        ref={fileInput}
        type="file"
        accept="image/*"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0]
          if (file) upload.mutate(file)
          e.target.value = ""
        }}
      />
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" loading={upload.isPending} onClick={() => fileInput.current?.click()}>
          {hasPicture.data ? "Change picture" : "Upload picture"}
        </Button>
        {hasPicture.data && (
          <Button variant="quiet" loading={remove.isPending} onClick={() => remove.mutate()}>
            Remove
          </Button>
        )}
      </div>
    </div>
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
  const [profiling, setProfiling] = useState(false)
  const [profileMailbox, setProfileMailbox] = useState("")
  const form = useForm<CreateValues>({ resolver: zodResolver(createSchema), defaultValues: { name: "", mailboxId: "" } })
  const writable = mailboxes.data?.filter((m) => m.access !== "read") ?? []

  const create = useMutation({
    mutationFn: (values: CreateValues) => api<CreatedAppPassword>("/me/app-passwords", { method: "POST", body: values }),
    onSuccess: (data) => {
      setCreated(data)
      form.reset()
      qc.invalidateQueries({ queryKey: ["app-passwords"] })
    },
  })
  const profile = useMutation({
    mutationFn: (mailboxId: string) => api<Blob>("/me/apple-profile", { method: "POST", body: { mailboxId }, blob: true }),
    onSuccess: (data) => {
      const address = writable.find((m) => m.id === profileMailbox)?.address ?? "aether"
      const url = URL.createObjectURL(data)
      const link = document.createElement("a")
      link.href = url
      link.download = `${address}.mobileconfig`
      link.click()
      URL.revokeObjectURL(url)
      toast.success("Profile downloaded", { description: "Open it on your iPhone, iPad or Mac to install it." })
      setProfiling(false)
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
    <section id="app-passwords" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader
        title="App passwords"
        description="Use an app password instead of your sign-in password in IMAP and SMTP mail clients. Each one works for a single mailbox."
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setProfileMailbox(writable.length === 1 ? writable[0].id : "")
                setProfiling(true)
              }}
            >
              <Smartphone aria-hidden />
              Set up an Apple device
            </Button>
            <Button variant="secondary" onClick={() => setCreating(true)}>
              <Plus aria-hidden />
              Create app password
            </Button>
          </>
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
                      {writable.map((m) => (
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

      <FormDialog
        open={profiling}
        onOpenChange={(open) => {
          setProfiling(open)
          if (!open) profile.reset()
        }}
        title="Set up an Apple device"
        description="Download a configuration profile that adds mail, calendar and contacts to an iPhone, iPad or Mac. It contains a new app password, which you can revoke here."
        submitLabel="Download profile"
        pending={profile.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (profileMailbox) profile.mutate(profileMailbox)
        }}
      >
        <Field id="profile-mailbox" label="Mailbox" error={profile.error ? errorMessage(profile.error) : undefined}>
          <Select value={profileMailbox} onValueChange={setProfileMailbox}>
            <SelectTrigger id="profile-mailbox" className="w-full">
              <SelectValue placeholder={mailboxes.isPending ? "Loading mailboxes…" : "Choose a mailbox"} />
            </SelectTrigger>
            <SelectContent>
              {writable.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  {m.address}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
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
      <CopyField id="ap-secret" label="Password" value={password} copyLabel="Copy password" />
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

const noSubscribe = () => () => {}

function CalendarApps() {
  const host = useSyncExternalStore(noSubscribe, () => window.location.host, () => "")
  const mailboxes = useQuery(myMailboxesQuery)
  const server = host && `https://${host}/dav/`
  const apps: [string, string][] = [
    ["iPhone and iPad", "In Settings, open the accounts list for Calendar, choose Add Account, then Other. Add a CalDAV account for your calendar and a CardDAV account for your contacts."],
    ["Mac", "In System Settings, open Internet Accounts, then Add Other Account. Add a CalDAV account and a CardDAV account."],
    ["Thunderbird", "For the calendar, choose New Calendar, then On the Network. For contacts, choose New Address Book, then Add CardDAV Address Book."],
    ["Android with DAVx⁵", "Add an account and choose Login with URL and user name, then pick the calendar and address book to sync."],
  ]

  return (
    <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <SectionHeader
        title="Calendar and contacts apps"
        description="Your Aether calendar and contacts sync to phone and desktop apps over CalDAV and CardDAV. These apps need an app password, not the password you use to sign in on the web."
      />
      <dl className="flex flex-col rounded-xl border border-border">
        <div className="flex flex-col gap-1 border-b border-border p-4 sm:flex-row sm:items-center sm:gap-4">
          <dt className="w-32 shrink-0 text-label text-fg-tertiary">Server address</dt>
          <dd className="flex min-w-0 items-center gap-2">
            <span className="font-mono text-sm break-all">{server}</span>
            {server && <CopyButton value={server} label="Copy server address" />}
          </dd>
        </div>
        <div className="flex flex-col gap-1 border-b border-border p-4 sm:flex-row sm:gap-4">
          <dt className="w-32 shrink-0 text-label text-fg-tertiary">Username</dt>
          <dd className="text-sm">
            Your full mailbox address
            {mailboxes.data?.length ? (
              <>
                , for example <span className="font-mono">{mailboxes.data[0].address}</span>
              </>
            ) : null}
          </dd>
        </div>
        <div className="flex flex-col gap-1 p-4 sm:flex-row sm:gap-4">
          <dt className="w-32 shrink-0 text-label text-fg-tertiary">Password</dt>
          <dd className="text-sm">
            An app password for that mailbox.{" "}
            <Button variant="link" asChild>
              <a href="#app-passwords">Create one under App passwords</a>
            </Button>
          </dd>
        </div>
      </dl>
      <ul className="flex flex-col gap-3">
        {apps.map(([name, steps]) => (
          <li key={name} className="flex flex-col gap-1">
            <span className="font-semibold">{name}</span>
            <span className="text-sm text-fg-secondary">{steps} Enter the server address, username and app password above.</span>
          </li>
        ))}
      </ul>
    </section>
  )
}

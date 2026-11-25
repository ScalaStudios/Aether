"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Ban, Ellipsis, KeyRound, MonitorSmartphone, Pencil, Plus, RotateCcw, ShieldOff, UserMinus, Users } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SessionList } from "@/components/settings/sessions"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api, ApiError, errorMessage } from "@/lib/api"
import { membersQuery } from "@/lib/queries"
import type { Member, Role } from "@/lib/types"

const roles: Record<Role, string> = { owner: "Owner", admin: "Administrator", member: "Member" }

const schema = z.object({
  email: z.email("Enter a valid email address."),
  role: z.enum(["owner", "admin", "member"]),
  compliance: z.boolean(),
})

type Values = z.infer<typeof schema>

export function MembersTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const members = useQuery(membersQuery(orgId))
  const [editing, setEditing] = useState<Member | "new" | null>(null)
  const [suspending, setSuspending] = useState<Member | null>(null)
  const [removing, setRemoving] = useState<Member | null>(null)
  const [sessionsOf, setSessionsOf] = useState<Member | null>(null)
  const [resettingPassword, setResettingPassword] = useState<Member | null>(null)
  const [resettingTwoFactor, setResettingTwoFactor] = useState<Member | null>(null)
  const [newPassword, setNewPassword] = useState("")
  const session = useSession()
  const platformAdmin = session.data?.user.isPlatformAdmin ?? false
  const owner = platformAdmin || session.data?.memberships.some((m) => m.orgId === orgId && m.role === "owner")

  const setStatus = useMutation({
    mutationFn: ({ member, status }: { member: Member; status: "active" | "suspended" }) =>
      api(`/admin/users/${member.userId}`, { method: "PATCH", body: { status } }),
    onSuccess: (_d, { member, status }) => {
      toast.success(status === "suspended" ? `Suspended ${member.email}` : `Restored ${member.email}`)
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      setSuspending(null)
    },
  })
  const remove = useMutation({
    mutationFn: (member: Member) => api(`/organizations/${orgId}/members/${member.userId}`, { method: "DELETE" }),
    meta: { silent: true },
    onSuccess: (_d, member) => {
      toast.success(`Removed ${member.email}`)
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      setRemoving(null)
    },
  })
  const resetPassword = useMutation({
    mutationFn: (member: Member) => api(`/admin/users/${member.userId}/password`, { method: "PUT", body: { password: newPassword } }),
    meta: { silent: true },
    onSuccess: (_d, member) => {
      toast.success(`Reset the password of ${member.email}`, { description: "They were signed out everywhere." })
      closePasswordReset()
    },
  })
  const resetTwoFactor = useMutation({
    mutationFn: (member: Member) => api(`/admin/users/${member.userId}/two-factor`, { method: "DELETE" }),
    onSuccess: (_d, member) => {
      toast.success(`Turned off two-factor authentication for ${member.email}`)
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      setResettingTwoFactor(null)
    },
  })
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { email: "", role: "member", compliance: false } })

  function closePasswordReset() {
    setResettingPassword(null)
    setNewPassword("")
    resetPassword.reset()
  }

  const save = useMutation({
    mutationFn: ({ compliance, ...values }: Values) =>
      api(`/organizations/${orgId}/members`, { method: "POST", body: owner ? { ...values, compliance } : values }),
    meta: { silent: true },
    onSuccess: (_d, values) => {
      toast.success(editing === "new" ? `Added ${values.email}` : `Changed the role of ${values.email}`)
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      close()
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "unknown_user") {
        form.setError("email", { message: "No Aether account uses this email. Create a mailbox for it, or ask the person to sign in once with single sign-on." })
      } else toast.error(errorMessage(error))
    },
  })

  function open(member: Member | "new") {
    form.reset(member === "new" ? { email: "", role: "member", compliance: false } : { email: member.email, role: member.role, compliance: member.compliance })
    setEditing(member)
  }

  function close() {
    setEditing(null)
    form.reset()
  }

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Members"
        description="People with access to this organization. Only owners can grant or change the owner role."
        actions={
          <Button onClick={() => open("new")}>
            <Plus aria-hidden />
            Add member
          </Button>
        }
      />
      <QueryView query={members} empty={<EmptyState icon={Users} title="No members" description="Add someone who already has an Aether account." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Email</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Two-factor</TableHead>
                <TableHead className="w-36">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((m) => (
                <TableRow key={m.userId}>
                  <TableCell className="font-medium">{m.displayName || "—"}</TableCell>
                  <TableCell>{m.email}</TableCell>
                  <TableCell>
                    <div className="flex flex-wrap items-center gap-2">
                      {roles[m.role]}
                      {m.compliance && <StatusBadge tone="info">Compliance</StatusBadge>}
                    </div>
                  </TableCell>
                  <TableCell>
                    <StatusBadge tone={m.status === "active" ? "success" : "warning"}>{m.status.charAt(0).toUpperCase() + m.status.slice(1)}</StatusBadge>
                  </TableCell>
                  <TableCell>
                    {m.twoFactor ? (
                      <StatusBadge tone="success">On</StatusBadge>
                    ) : m.passwordSignIn ? (
                      <StatusBadge tone="neutral">Off</StatusBadge>
                    ) : (
                      <span className="text-sm text-fg-tertiary">Identity provider</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex gap-1">
                      <IconButton label={`Change role of ${m.email}`} size="icon-sm" onClick={() => open(m)}>
                        <Pencil />
                      </IconButton>
                      {platformAdmin && m.userId !== session.data?.user.id && (
                        m.status === "active" ? (
                          <IconButton label={`Suspend ${m.email}`} size="icon-sm" onClick={() => setSuspending(m)}>
                            <Ban />
                          </IconButton>
                        ) : (
                          <IconButton label={`Restore ${m.email}`} size="icon-sm" disabled={setStatus.isPending} onClick={() => setStatus.mutate({ member: m, status: "active" })}>
                            <RotateCcw />
                          </IconButton>
                        )
                      )}
                      {m.userId !== session.data?.user.id && (
                        <IconButton label={`Remove ${m.email}`} size="icon-sm" onClick={() => setRemoving(m)}>
                          <UserMinus />
                        </IconButton>
                      )}
                      {(m.role !== "owner" || owner) && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="quiet" size="icon-sm" aria-label={`More actions for ${m.email}`}>
                              <Ellipsis />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end" className="w-56">
                            <DropdownMenuItem onSelect={() => setSessionsOf(m)}>
                              <MonitorSmartphone aria-hidden />
                              Sessions…
                            </DropdownMenuItem>
                            {platformAdmin && m.userId !== session.data?.user.id && m.passwordSignIn && (
                              <DropdownMenuItem onSelect={() => setResettingPassword(m)}>
                                <KeyRound aria-hidden />
                                Reset password…
                              </DropdownMenuItem>
                            )}
                            {platformAdmin && m.userId !== session.data?.user.id && m.twoFactor && (
                              <DropdownMenuItem onSelect={() => setResettingTwoFactor(m)}>
                                <ShieldOff aria-hidden />
                                Turn off two-factor…
                              </DropdownMenuItem>
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

      <FormDialog
        open={editing !== null}
        onOpenChange={(next) => !next && close()}
        title={editing === "new" ? "Add member" : "Change role"}
        description={editing === "new" ? "The person needs an existing Aether account." : undefined}
        submitLabel={editing === "new" ? "Add member" : "Save role"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
      >
        <Field id="member-email" label="Email" error={form.formState.errors.email?.message}>
          <Input
            id="member-email"
            type="email"
            autoComplete="off"
            readOnly={editing !== "new"}
            aria-invalid={!!form.formState.errors.email}
            {...form.register("email")}
          />
        </Field>
        <Controller
          control={form.control}
          name="role"
          render={({ field }) => (
            <Field id="member-role" label="Role">
              <Select value={field.value} onValueChange={field.onChange}>
                <SelectTrigger id="member-role" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(roles) as Role[]).map((r) => (
                    <SelectItem key={r} value={r}>
                      {roles[r]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
        />
        {owner && (
          <SwitchField
            control={form.control}
            name="compliance"
            id="member-compliance"
            label="Compliance administrator"
            hint="Can place legal holds, search every mailbox in this organization, export the results and set up journaling. Every action is recorded in the audit log."
          />
        )}
      </FormDialog>
      <Dialog open={!!sessionsOf} onOpenChange={(o) => !o && setSessionsOf(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Sessions of {sessionsOf?.email}</DialogTitle>
            <DialogDescription>Browsers signed in to this account. Signing a session out ends it at once; app passwords for mail apps keep working.</DialogDescription>
          </DialogHeader>
          {sessionsOf && <SessionList path={`/organizations/${orgId}/members/${sessionsOf.userId}/sessions`} subject={sessionsOf.email} />}
        </DialogContent>
      </Dialog>
      <FormDialog
        open={!!resettingPassword}
        onOpenChange={(o) => !o && closePasswordReset()}
        title={`Reset the password of ${resettingPassword?.email}?`}
        description="They are signed out everywhere and sign in with the new password. Tell them the password through a channel you trust. Their two-factor authentication stays on."
        submitLabel="Reset password"
        pending={resetPassword.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (resettingPassword && newPassword.length >= 12) resetPassword.mutate(resettingPassword)
        }}
      >
        <Field
          id="reset-password"
          label="New password"
          hint="At least 12 characters."
          error={resetPassword.error ? errorMessage(resetPassword.error) : newPassword && newPassword.length < 12 ? "Use at least 12 characters." : undefined}
        >
          <Input id="reset-password" type="password" autoComplete="new-password" aria-invalid={resetPassword.isError} value={newPassword} onChange={(e) => setNewPassword(e.target.value)} />
        </Field>
      </FormDialog>
      <ConfirmDialog
        open={!!resettingTwoFactor}
        onOpenChange={(o) => !o && setResettingTwoFactor(null)}
        title={`Turn off two-factor authentication for ${resettingTwoFactor?.email}?`}
        description="Removes their authenticator app, security keys and recovery codes, for example after they lost their phone. They sign in with their password alone until they set it up again; if an organization requires it, they are asked to at their next sign-in."
        confirmLabel="Turn off two-factor"
        pending={resetTwoFactor.isPending}
        onConfirm={() => resettingTwoFactor && resetTwoFactor.mutate(resettingTwoFactor)}
      />
      <ConfirmDialog
        open={!!suspending}
        onOpenChange={(o) => !o && setSuspending(null)}
        title={`Suspend ${suspending?.email}?`}
        description="They are signed out everywhere, cannot sign in, and their app passwords stop working for IMAP and SMTP. This applies in every organization they belong to. Mail keeps arriving in their mailboxes."
        confirmLabel="Suspend account"
        pending={setStatus.isPending}
        onConfirm={() => suspending && setStatus.mutate({ member: suspending, status: "suspended" })}
      />
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(o) => {
          if (o) return
          setRemoving(null)
          remove.reset()
        }}
        title={`Remove ${removing?.email}?`}
        description="They lose their role here, their delegated access to this organization’s mailboxes, and ownership of its distribution lists. Their account and the mailboxes they own aren’t deleted."
        confirmLabel="Remove member"
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing)}
      >
        {remove.error && (
          <p role="alert" className="text-sm text-danger">
            {errorMessage(remove.error)}
          </p>
        )}
      </ConfirmDialog>
    </section>
  )
}

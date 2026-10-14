"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Ban, Pencil, Plus, RotateCcw, Users } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api, ApiError, errorMessage } from "@/lib/api"
import type { Member, Role } from "@/lib/types"

const roles: Record<Role, string> = { owner: "Owner", admin: "Administrator", member: "Member" }

const schema = z.object({
  email: z.email("Enter a valid email address."),
  role: z.enum(["owner", "admin", "member"]),
})

type Values = z.infer<typeof schema>

export function MembersTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const members = useQuery({ queryKey: ["members", orgId], queryFn: () => api<Member[]>(`/organizations/${orgId}/members`) })
  const [editing, setEditing] = useState<Member | "new" | null>(null)
  const [suspending, setSuspending] = useState<Member | null>(null)
  const session = useSession()
  const platformAdmin = session.data?.user.isPlatformAdmin ?? false

  const setStatus = useMutation({
    mutationFn: ({ member, status }: { member: Member; status: "active" | "suspended" }) =>
      api(`/admin/users/${member.userId}`, { method: "PATCH", body: { status } }),
    onSuccess: (_d, { member, status }) => {
      toast.success(status === "suspended" ? `Suspended ${member.email}` : `Restored ${member.email}`)
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      setSuspending(null)
    },
  })
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { email: "", role: "member" } })

  const save = useMutation({
    mutationFn: (values: Values) => api(`/organizations/${orgId}/members`, { method: "POST", body: values }),
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
    form.reset(member === "new" ? { email: "", role: "member" } : { email: member.email, role: member.role })
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
                <TableHead className="w-20">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((m) => (
                <TableRow key={m.userId}>
                  <TableCell className="font-medium">{m.displayName || "—"}</TableCell>
                  <TableCell>{m.email}</TableCell>
                  <TableCell>{roles[m.role]}</TableCell>
                  <TableCell>
                    <StatusBadge tone={m.status === "active" ? "success" : "warning"}>{m.status.charAt(0).toUpperCase() + m.status.slice(1)}</StatusBadge>
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
      </FormDialog>
      <ConfirmDialog
        open={!!suspending}
        onOpenChange={(o) => !o && setSuspending(null)}
        title={`Suspend ${suspending?.email}?`}
        description="They are signed out everywhere, cannot sign in, and their app passwords stop working for IMAP and SMTP. This applies in every organization they belong to. Mail keeps arriving in their mailboxes."
        confirmLabel="Suspend account"
        pending={setStatus.isPending}
        onConfirm={() => suspending && setStatus.mutate({ member: suspending, status: "suspended" })}
      />
    </section>
  )
}

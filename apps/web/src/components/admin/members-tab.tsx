"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Ban, KeyRound, MonitorSmartphone, Pencil, Plus, RefreshCw, RotateCcw, ShieldOff, UserMinus, Users } from "lucide-react"
import { useState } from "react"
import { Controller, useForm, useWatch } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { DomainChecklist } from "@/components/admin/domain-checklist"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { CopyButton } from "@/components/copy-button"
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
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api, ApiError, errorMessage } from "@/lib/api"
import { useI18n } from "@/lib/i18n"
import { plural } from "@/lib/format"
import {
  BUILT_IN_ROLES,
  canEditTargetMember,
  canSupportTarget,
  hasPerm,
  orgMembership,
  ROLE_DESCRIPTIONS,
  ROLE_LABELS,
  roleBadgeTone,
  roleLabel,
} from "@/lib/permissions"
import { domainsQuery, membersQuery, rolesQuery } from "@/lib/queries"
import type { BuiltInRole, Member, Role } from "@/lib/types"

const CUSTOM_PREFIX = "custom:"

const memberSchema = z
  .object({
    email: z.email("Enter a valid email address."),
    roleChoice: z.string().min(1, "Choose a role."),
    domainIds: z.array(z.string()),
    compliance: z.boolean(),
  })
  .refine((v) => v.roleChoice !== "domain_admin" || v.domainIds.length > 0, {
    message: "Choose at least one domain for a domain administrator.",
    path: ["domainIds"],
  })

type MemberValues = z.infer<typeof memberSchema>

const passwordSchema = z.object({
  password: z.string().min(12, "Use at least 12 characters."),
})

type PasswordValues = z.infer<typeof passwordSchema>

function generatePassword(length = 20): string {
  const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*-_"
  const bytes = new Uint8Array(length)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join("")
}

function choiceFromMember(member: Member): string {
  if (member.role === "custom" && member.customRoleId) return `${CUSTOM_PREFIX}${member.customRoleId}`
  return member.role
}

function parseChoice(choice: string): { role: Role; customRoleId?: string } {
  if (choice.startsWith(CUSTOM_PREFIX)) {
    return { role: "custom", customRoleId: choice.slice(CUSTOM_PREFIX.length) }
  }
  return { role: choice as BuiltInRole }
}

export function MembersTab({ orgId }: { orgId: string }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const session = useSession().data
  const platformAdmin = session?.user.isPlatformAdmin ?? false
  const callerRole = orgMembership(session, orgId)?.role
  const owner = platformAdmin || callerRole === "owner"
  const [sessionsOf, setSessionsOf] = useState<Member | null>(null)
  const [resettingTwoFactor, setResettingTwoFactor] = useState<Member | null>(null)
  const canWrite = hasPerm(session, orgId, "members:write")
  const canSupport = hasPerm(session, orgId, "members:support")
  const canReadDomains = hasPerm(session, orgId, "domains:read", "members:write")
  const canReadRoles = hasPerm(session, orgId, "roles:read", "members:write")

  const members = useQuery(membersQuery(orgId))
  const domains = useQuery({ ...domainsQuery(orgId), enabled: canReadDomains })
  const orgRoles = useQuery({ ...rolesQuery(orgId), enabled: canReadRoles })

  const [editing, setEditing] = useState<Member | "new" | null>(null)
  const [suspending, setSuspending] = useState<Member | null>(null)
  const [resetting, setResetting] = useState<Member | null>(null)
  const [removing, setRemoving] = useState<Member | null>(null)

  const form = useForm<MemberValues>({
    resolver: zodResolver(memberSchema),
    defaultValues: { email: "", roleChoice: "member", domainIds: [], compliance: false },
  })
  const roleChoice = useWatch({ control: form.control, name: "roleChoice" })

  const passwordForm = useForm<PasswordValues>({
    resolver: zodResolver(passwordSchema),
    defaultValues: { password: "" },
  })
  const draftPassword = useWatch({ control: passwordForm.control, name: "password" })

  const assignableBuiltIn: BuiltInRole[] = BUILT_IN_ROLES.filter((r) => {
    if (platformAdmin || callerRole === "owner") return true
    if (callerRole === "admin") return r !== "owner"
    return r !== "owner" && r !== "admin"
  })

  const setStatus = useMutation({
    mutationFn: ({ member, status }: { member: Member; status: "active" | "suspended" }) =>
      platformAdmin
        ? api(`/admin/users/${member.userId}`, { method: "PATCH", body: { status } })
        : api(`/organizations/${orgId}/members/${member.userId}/status`, { method: "PATCH", body: { status } }),
    onSuccess: (_d, { member, status }) => {
      toast.success(status === "suspended" ? t("admin.members.suspendedToast", { email: member.email }) : t("admin.members.restoredToast", { email: member.email }))
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      setSuspending(null)
    },
  })

  const resetPassword = useMutation({
    mutationFn: ({ member, password }: { member: Member; password: string }) =>
      api(`/organizations/${orgId}/members/${member.userId}/password`, { method: "POST", body: { password } }),
    meta: { silent: true },
    onSuccess: (_d, { member }) => {
      toast.success(`Password updated for ${member.email}`)
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      setResetting(null)
      passwordForm.reset()
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "weak_password") {
        passwordForm.setError("password", { message: error.message })
      } else {
        toast.error(errorMessage(error))
      }
    },
  })

  const remove = useMutation({
    mutationFn: (member: Member) => api(`/organizations/${orgId}/members/${member.userId}`, { method: "DELETE" }),
    meta: { silent: true },
    onSuccess: (_d, member) => {
      toast.success(t("admin.members.removedToast", { email: member.email }))
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      qc.invalidateQueries({ queryKey: ["roles", orgId] })
      qc.invalidateQueries({ queryKey: ["session"] })
      setRemoving(null)
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

  const save = useMutation({
    mutationFn: (values: MemberValues) => {
      const { role, customRoleId } = parseChoice(values.roleChoice)
      const domainIds = role === "domain_admin" || role === "custom" ? values.domainIds : []
      return api(`/organizations/${orgId}/members`, {
        method: "POST",
        body: owner ? { email: values.email, role, customRoleId, domainIds, compliance: values.compliance } : { email: values.email, role, customRoleId, domainIds },
      })
    },
    meta: { silent: true },
    onSuccess: (_d, values) => {
      toast.success(editing === "new" ? `Added ${values.email}` : `Updated role for ${values.email}`)
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      qc.invalidateQueries({ queryKey: ["roles", orgId] })
      qc.invalidateQueries({ queryKey: ["session"] })
      close()
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === "unknown_user") {
        form.setError("email", { message: t("admin.members.unknownUser") })
      } else if (error instanceof ApiError && error.code === "invalid_domains") {
        form.setError("domainIds", { message: error.message })
      } else if (error instanceof ApiError && error.code === "self_membership") {
        form.setError("email", { message: error.message })
      } else {
        toast.error(errorMessage(error))
      }
    },
  })

  function open(member: Member | "new") {
    if (member === "new") {
      form.reset({ email: "", roleChoice: "member", domainIds: [], compliance: false })
    } else {
      form.reset({
        email: member.email,
        roleChoice: choiceFromMember(member),
        domainIds: member.domainIds,
        compliance: member.compliance,
      })
    }
    setEditing(member)
  }

  function close() {
    setEditing(null)
    form.reset()
  }

  function domainNames(ids: string[]): string[] {
    if (!domains.data) return []
    const byId = new Map(domains.data.map((d) => [d.id, d.name]))
    return ids.map((id) => byId.get(id)).filter((n): n is string => Boolean(n))
  }

  const parsedRole = parseChoice(roleChoice || "member")
  const showDomains = parsedRole.role === "domain_admin" || parsedRole.role === "custom"
  const roleHint =
    parsedRole.role === "custom"
      ? orgRoles.data?.find((r) => r.id === parsedRole.customRoleId)?.description || "Custom organization role."
      : ROLE_DESCRIPTIONS[parsedRole.role as BuiltInRole]

  const hasAnyRowAction = canWrite || canSupport

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title={t("admin.members.title")}
        description={
          canWrite
            ? "People with access to this organization, their administrative roles and optional domain scopes."
            : canSupport
              ? "Support ordinary members by resetting passwords or suspending and restoring accounts."
              : "People with access to this organization."
        }
        actions={
          canWrite && (
            <Button onClick={() => open("new")}>
              <Plus aria-hidden />
              {t("admin.members.addMember")}
            </Button>
          )
        }
      />
      <QueryView query={members} empty={<EmptyState icon={Users} title={t("admin.members.emptyTitle")} description={t("admin.members.emptyDescription")} />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("common.name")}</TableHead>
                <TableHead>{t("common.email")}</TableHead>
                <TableHead>{t("admin.members.role")}</TableHead>
                <TableHead>{t("common.status")}</TableHead>
                <TableHead>Two-factor</TableHead>
                {hasAnyRowAction && (
                  <TableHead className="w-36">
                    <span className="sr-only">{t("common.actions")}</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((m) => {
                const canEdit = canEditTargetMember(session, orgId, m)
                const canSupportMember = canSupportTarget(session, orgId, m)
                const scopedNames = domainNames(m.domainIds)
                return (
                  <TableRow key={m.userId}>
                    <TableCell className="font-medium">{m.displayName || "—"}</TableCell>
                    <TableCell className="font-mono text-sm">{m.email}</TableCell>
                    <TableCell className="whitespace-normal">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <StatusBadge tone={roleBadgeTone(m.role)}>{roleLabel(m.role, m.customRoleName)}</StatusBadge>
                        {m.compliance && <StatusBadge tone="info">Compliance</StatusBadge>}
                        {m.domainIds.length > 0 &&
                          (scopedNames.length > 0 ? (
                            scopedNames.map((name) => (
                              <span key={name} className="rounded-xs bg-muted px-2 py-0.5 font-mono text-xs text-fg-secondary">
                                {name}
                              </span>
                            ))
                          ) : (
                            <span className="text-xs text-fg-tertiary">{plural(m.domainIds.length, "domain")}</span>
                          ))}
                      </div>
                    </TableCell>
                    <TableCell>
                      <StatusBadge tone={m.status === "active" ? "success" : "warning"}>{t(`common.userStatus.${m.status}`)}</StatusBadge>
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
                    {hasAnyRowAction && (
                      <TableCell>
                        <div className="flex gap-1">
                          {canEdit && (
                            <IconButton label={`Change role of ${m.email}`} size="icon-sm" onClick={() => open(m)}>
                              <Pencil />
                            </IconButton>
                          )}
                          {canSupportMember && m.status !== "deprovisioned" && (
                            <IconButton
                              label={`Reset password for ${m.email}`}
                              size="icon-sm"
                              onClick={() => {
                                passwordForm.reset({ password: "" })
                                setResetting(m)
                              }}
                            >
                              <KeyRound />
                            </IconButton>
                          )}
                          {canSupportMember &&
                            (m.status === "active" ? (
                              <IconButton label={`Suspend ${m.email}`} size="icon-sm" onClick={() => setSuspending(m)}>
                                <Ban />
                              </IconButton>
                            ) : m.status === "suspended" ? (
                              <IconButton label={`Restore ${m.email}`} size="icon-sm" disabled={setStatus.isPending} onClick={() => setStatus.mutate({ member: m, status: "active" })}>
                                <RotateCcw />
                              </IconButton>
                            ) : null)}
                          {canSupportMember && (
                            <IconButton label={`Sessions of ${m.email}`} size="icon-sm" onClick={() => setSessionsOf(m)}>
                              <MonitorSmartphone />
                            </IconButton>
                          )}
                          {platformAdmin && m.userId !== session?.user.id && m.twoFactor && (
                            <IconButton label={`Turn off two-factor for ${m.email}`} size="icon-sm" onClick={() => setResettingTwoFactor(m)}>
                              <ShieldOff />
                            </IconButton>
                          )}
                          {canEdit && m.userId !== session?.user.id && (
                            <IconButton label={t("admin.members.removeAria", { email: m.email })} size="icon-sm" onClick={() => setRemoving(m)}>
                              <UserMinus />
                            </IconButton>
                          )}
                        </div>
                      </TableCell>
                    )}
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </QueryView>

      <FormDialog
        open={editing !== null}
        onOpenChange={(next) => !next && close()}
        title={editing === "new" ? t("admin.members.addMember") : t("admin.members.changeRoleTitle")}
        description={editing === "new" ? t("admin.members.addDescription") : undefined}
        submitLabel={editing === "new" ? t("admin.members.addMember") : t("admin.members.saveRole")}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
      >
        <Field id="member-email" label={t("common.email")} error={form.formState.errors.email?.message}>
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
          name="roleChoice"
          render={({ field }) => (
            <Field id="member-role" label="Role" hint={roleHint}>
              <Select
                value={field.value}
                onValueChange={(next) => {
                  field.onChange(next)
                  if (next !== "domain_admin" && !next.startsWith(CUSTOM_PREFIX)) {
                    form.setValue("domainIds", [])
                  }
                }}
              >
                <SelectTrigger id="member-role" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectLabel>Built-in roles</SelectLabel>
                    {assignableBuiltIn.map((r) => (
                      <SelectItem key={r} value={r}>
                        {ROLE_LABELS[r]}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                  {orgRoles.data && orgRoles.data.length > 0 && (
                    <>
                      <SelectSeparator />
                      <SelectGroup>
                        <SelectLabel>Custom roles</SelectLabel>
                        {orgRoles.data.map((r) => (
                          <SelectItem key={r.id} value={`${CUSTOM_PREFIX}${r.id}`}>
                            {r.name}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </>
                  )}
                </SelectContent>
              </Select>
            </Field>
          )}
        />
        {showDomains && (
          <Controller
            control={form.control}
            name="domainIds"
            render={({ field, fieldState }) => (
              <DomainChecklist
                domains={domains.data}
                loading={domains.isPending}
                value={field.value}
                onChange={field.onChange}
                label={parsedRole.role === "domain_admin" ? "Domains" : "Limit to domains (optional)"}
                hint={
                  parsedRole.role === "domain_admin"
                    ? "Choose at least one domain this administrator can manage."
                    : "Leave all unchecked for organization-wide access, or choose specific domains."
                }
                error={fieldState.error?.message}
              />
            )}
          />
        )}
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
      <ConfirmDialog
        open={!!resettingTwoFactor}
        onOpenChange={(o) => !o && setResettingTwoFactor(null)}
        title={`Turn off two-factor authentication for ${resettingTwoFactor?.email}?`}
        description="Removes their authenticator app, security keys and recovery codes, for example after they lost their phone. They sign in with their password alone until they set it up again; if an organization requires it, they are asked to at their next sign-in."
        confirmLabel="Turn off two-factor"
        pending={resetTwoFactor.isPending}
        onConfirm={() => resettingTwoFactor && resetTwoFactor.mutate(resettingTwoFactor)}
      />

      <FormDialog
        open={!!resetting}
        onOpenChange={(open) => {
          if (!open) {
            setResetting(null)
            passwordForm.reset()
          }
        }}
        title={`Reset password for ${resetting?.email}`}
        description="Sets a new web sign-in password and signs the user out of all active sessions. App passwords are not changed."
        submitLabel="Set password"
        pending={resetPassword.isPending}
        onSubmit={passwordForm.handleSubmit((v) => resetting && resetPassword.mutate({ member: resetting, password: v.password }))}
      >
        <Field id="reset-password" label="New password" hint="At least 12 characters." error={passwordForm.formState.errors.password?.message}>
          <div className="flex items-center gap-2">
            <Input
              id="reset-password"
              type="text"
              autoComplete="new-password"
              spellCheck={false}
              className="font-mono"
              aria-invalid={!!passwordForm.formState.errors.password}
              {...passwordForm.register("password")}
            />
            {draftPassword && <CopyButton value={draftPassword} label="Copy password" />}
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => passwordForm.setValue("password", generatePassword(), { shouldValidate: true })}
            >
              <RefreshCw aria-hidden />
              Generate
            </Button>
          </div>
        </Field>
      </FormDialog>

      <ConfirmDialog
        open={!!suspending}
        onOpenChange={(o) => !o && setSuspending(null)}
        title={t("admin.members.suspendTitle", { email: suspending?.email ?? "" })}
        description="They are signed out everywhere, cannot sign in, and their app passwords stop working for IMAP and SMTP. Mail keeps arriving in their mailboxes."
        confirmLabel={t("admin.members.suspendConfirm")}
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
        title={t("admin.members.removeTitle", { email: removing?.email ?? "" })}
        description={t("admin.members.removeDescription")}
        confirmLabel={t("admin.members.removeConfirm")}
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

"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil, Plus, Shield, Trash2 } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useSession } from "@/hooks/use-session"
import { api, ApiError, errorMessage } from "@/lib/api"
import { plural } from "@/lib/format"
import {
  BUILT_IN_ROLES,
  hasPerm,
  ROLE_DESCRIPTIONS,
  ROLE_LABELS,
  roleBadgeTone,
  SCOPE_LEVEL_LABELS,
  SCOPE_RESOURCES,
  type ScopeAccess,
} from "@/lib/permissions"
import { rolesQuery } from "@/lib/queries"
import type { OrgRole } from "@/lib/types"

const schema = z.object({
  name: z.string().trim().min(1, "Enter a role name.").max(80, "Use 80 characters or fewer."),
  description: z.string().trim().max(240, "Use 240 characters or fewer."),
  access: z.record(z.string(), z.enum(["", "read", "write"])).refine((a) => Object.values(a).some(Boolean), "Choose at least one permission."),
})

type Values = z.infer<typeof schema>

const chip = "rounded-xs bg-muted px-2 py-1 font-mono text-xs"

function emptyAccess(): Record<string, ScopeAccess> {
  return Object.fromEntries(SCOPE_RESOURCES.map((r) => [r.value, "" as ScopeAccess]))
}

function accessFromScopes(scopes: string[]): Record<string, ScopeAccess> {
  const access = emptyAccess()
  for (const s of scopes) {
    const [resource, level] = s.split(":")
    if (resource in access && (level === "read" || level === "write")) {
      access[resource] = level
    }
  }
  return access
}

function scopesFromAccess(access: Record<string, ScopeAccess>): string[] {
  return Object.entries(access).flatMap(([resource, level]) => (level ? [`${resource}:${level}`] : []))
}

export function RolesTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const session = useSession().data
  const canWrite = hasPerm(session, orgId, "roles:write")
  const roles = useQuery(rolesQuery(orgId))
  const [editing, setEditing] = useState<OrgRole | "new" | null>(null)
  const [deleting, setDeleting] = useState<OrgRole | null>(null)
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", description: "", access: emptyAccess() },
  })
  const existing = editing && editing !== "new" ? editing : null

  function open(target: OrgRole | "new") {
    if (target === "new") {
      form.reset({ name: "", description: "", access: emptyAccess() })
    } else {
      form.reset({ name: target.name, description: target.description, access: accessFromScopes(target.scopes) })
    }
    setEditing(target)
  }

  function close() {
    setEditing(null)
    form.reset()
  }

  const save = useMutation({
    mutationFn: (v: Values) => {
      const body = { name: v.name, description: v.description, scopes: scopesFromAccess(v.access) }
      return existing
        ? api<OrgRole>(`/roles/${existing.id}`, { method: "PUT", body })
        : api<OrgRole>(`/organizations/${orgId}/roles`, { method: "POST", body })
    },
    meta: { silent: true },
    onSuccess: (saved) => {
      toast.success(existing ? `Saved ${saved.name}` : `Created ${saved.name}`)
      qc.invalidateQueries({ queryKey: ["roles", orgId] })
      qc.invalidateQueries({ queryKey: ["members", orgId] })
      close()
    },
    onError: (error) => {
      if (error instanceof ApiError && (error.code === "role_exists" || error.code === "invalid_name")) {
        form.setError("name", { message: error.message })
      } else {
        toast.error(errorMessage(error))
      }
    },
  })

  const remove = useMutation({
    mutationFn: (role: OrgRole) => api(`/roles/${role.id}`, { method: "DELETE" }),
    meta: { silent: true },
    onSuccess: (_d, role) => {
      toast.success(`Deleted ${role.name}`)
      qc.invalidateQueries({ queryKey: ["roles", orgId] })
      setDeleting(null)
    },
  })

  const builtInAdminRoles = BUILT_IN_ROLES.filter((r) => r !== "member")

  return (
    <div className="flex flex-col gap-12">
      <section className="flex flex-col gap-6">
        <SectionHeader
          title="Custom roles"
          description="Compose roles from individual permissions and assign them under Members, optionally limited to specific domains."
          actions={
            canWrite && (
              <Button onClick={() => open("new")}>
                <Plus aria-hidden />
                Create role
              </Button>
            )
          }
        />
        <QueryView
          query={roles}
          empty={
            <EmptyState
              icon={Shield}
              title="No custom roles"
              description="Create a custom role when the built-in roles are broader or narrower than your team needs."
            />
          }
        >
          {(items) => (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Role</TableHead>
                  <TableHead>Permissions</TableHead>
                  <TableHead>Assigned</TableHead>
                  {canWrite && (
                    <TableHead className="w-20">
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  )}
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell className="whitespace-normal">
                      <div className="flex flex-col gap-0.5">
                        <span className="font-medium">{r.name}</span>
                        {r.description && <span className="text-sm text-fg-tertiary">{r.description}</span>}
                      </div>
                    </TableCell>
                    <TableCell className="whitespace-normal">
                      <div className="flex flex-wrap gap-1">
                        {r.scopes.map((s) => (
                          <span key={s} className={chip}>
                            {s}
                          </span>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell>{plural(r.memberCount, "member")}</TableCell>
                    {canWrite && (
                      <TableCell>
                        <div className="flex gap-1">
                          <IconButton label={`Edit ${r.name}`} size="icon-sm" onClick={() => open(r)}>
                            <Pencil />
                          </IconButton>
                          <IconButton label={`Delete ${r.name}`} size="icon-sm" onClick={() => setDeleting(r)}>
                            <Trash2 />
                          </IconButton>
                        </div>
                      </TableCell>
                    )}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </QueryView>
      </section>

      <section className="flex flex-col gap-6">
        <SectionHeader
          title="Built-in roles"
          description="Standard roles available in every organization. Assign them under Members or map them from identity provider groups."
        />
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Role</TableHead>
              <TableHead>Scope</TableHead>
              <TableHead>Access</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {builtInAdminRoles.map((r) => (
              <TableRow key={r}>
                <TableCell>
                  <StatusBadge tone={roleBadgeTone(r)}>{ROLE_LABELS[r]}</StatusBadge>
                </TableCell>
                <TableCell className="text-sm text-fg-secondary">
                  {r === "domain_admin" ? "Chosen domains" : "Organization"}
                </TableCell>
                <TableCell className="whitespace-normal text-sm text-fg-secondary">{ROLE_DESCRIPTIONS[r]}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </section>

      <FormDialog
        open={editing !== null}
        onOpenChange={(next) => !next && close()}
        wide
        title={existing ? `Edit ${existing.name}` : "Create custom role"}
        description="A write permission includes read access for that resource. Domain scopes can be chosen per member when assigning this role."
        submitLabel={existing ? "Save role" : "Create role"}
        pending={save.isPending}
        onSubmit={form.handleSubmit((v) => save.mutate(v))}
      >
        <Field id="role-name" label="Name" error={form.formState.errors.name?.message}>
          <Input id="role-name" placeholder="DNS operator" autoComplete="off" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
        </Field>
        <Field id="role-desc" label="Description" hint="Optional. Shown in the roles list." error={form.formState.errors.description?.message}>
          <Input id="role-desc" placeholder="Manages domain verification and reviews audit logs." autoComplete="off" aria-invalid={!!form.formState.errors.description} {...form.register("description")} />
        </Field>
        <Controller
          control={form.control}
          name="access"
          render={({ field, fieldState }) => (
            <fieldset className="flex flex-col gap-4">
              <legend className="mb-2 text-label text-fg-secondary">Permissions</legend>
              {SCOPE_RESOURCES.map((r) => (
                <div key={r.value} className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
                  <div className="flex flex-col gap-1">
                    <span>{r.label}</span>
                    <span className="text-sm text-fg-tertiary">{r.description}</span>
                  </div>
                  <div className="shrink-0">
                    <SegmentedControl
                      label={`${r.label} access`}
                      options={r.levels.map((level) => ({ value: level, label: SCOPE_LEVEL_LABELS[level] }))}
                      value={field.value[r.value]}
                      onChange={(level) => field.onChange({ ...field.value, [r.value]: level })}
                    />
                  </div>
                </div>
              ))}
              {fieldState.error && (
                <p role="alert" className="text-sm text-danger">
                  {fieldState.error.message}
                </p>
              )}
            </fieldset>
          )}
        />
      </FormDialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => {
          if (open) return
          setDeleting(null)
          remove.reset()
        }}
        title={`Delete ${deleting?.name}?`}
        description="Roles currently assigned to members or identity provider group mappings cannot be deleted until those assignments are changed."
        confirmLabel="Delete role"
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      >
        {remove.error && (
          <p role="alert" className="text-sm text-danger">
            {errorMessage(remove.error)}
          </p>
        )}
      </ConfirmDialog>
    </div>
  )
}

"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { RecipientInput } from "@/components/recipient-input"
import { ErrorState } from "@/components/states"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, ApiError, errorMessage } from "@/lib/api"
import { useI18n } from "@/lib/i18n"
import type { Address, Alias } from "@/lib/types"

export function ManagedLists() {
  const { t, tp } = useI18n()
  const qc = useQueryClient()
  const lists = useQuery({ queryKey: ["my-lists"], queryFn: () => api<Alias[]>("/me/lists") })
  const [editing, setEditing] = useState<Alias | null>(null)
  const [members, setMembers] = useState<Address[]>([])
  const [error, setError] = useState("")

  const save = useMutation({
    mutationFn: (list: Alias) => api<Alias>(`/aliases/${list.id}`, { method: "PATCH", body: { destinations: members.map((m) => m.address) } }),
    meta: { silent: true },
    onSuccess: (saved) => {
      toast.success(t("settings.lists.savedToast", { address: saved.address }))
      qc.invalidateQueries({ queryKey: ["my-lists"] })
      setEditing(null)
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 422) setError(e.message)
      else toast.error(errorMessage(e))
    },
  })

  function open(list: Alias) {
    setMembers(list.destinations.map((address) => ({ address })))
    setError("")
    setEditing(list)
  }

  const items = lists.data ?? []
  if (!lists.isError && !items.length) return null

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader title={t("settings.lists.title")} description={t("settings.lists.description")} />
      {lists.isError ? (
        <ErrorState error={lists.error} onRetry={() => lists.refetch()} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("settings.lists.listColumn")}</TableHead>
              <TableHead>{t("settings.lists.membersColumn")}</TableHead>
              <TableHead className="w-12">
                <span className="sr-only">{t("common.actions")}</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((l) => (
              <TableRow key={l.id}>
                <TableCell>
                  <div className="flex min-w-0 flex-col">
                    <span className="truncate font-medium">{l.name || l.address}</span>
                    {l.name && <span className="truncate font-mono text-sm text-fg-tertiary">{l.address}</span>}
                  </div>
                </TableCell>
                <TableCell>{tp("settings.lists.memberCount", l.destinations.length)}</TableCell>
                <TableCell>
                  <IconButton label={t("settings.lists.editAria", { address: l.address })} size="icon-sm" onClick={() => open(l)}>
                    <Pencil />
                  </IconButton>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <FormDialog
        open={!!editing}
        onOpenChange={(o) => !o && setEditing(null)}
        title={t("settings.lists.dialogTitle", { name: editing?.name || editing?.address || "" })}
        description={editing?.name ? <span className="font-mono">{editing.address}</span> : undefined}
        submitLabel={t("settings.lists.saveMembers")}
        pending={save.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (!members.length) setError(t("settings.lists.atLeastOne"))
          else if (editing) save.mutate(editing)
        }}
      >
        <Field id="list-members" label={t("settings.lists.membersColumn")} hint={t("settings.lists.membersHint")} error={error}>
          <RecipientInput
            id="list-members"
            value={members}
            onChange={(next) => {
              setMembers(next)
              setError("")
            }}
            invalid={!!error}
            placeholder={t("common.emailPlaceholder")}
            suggest
          />
        </Field>
      </FormDialog>
    </section>
  )
}

"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Settings2 } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { api } from "@/lib/api"
import { domainsQuery, personalAliasPolicyQuery } from "@/lib/queries"
import type { PersonalAliasPolicy } from "@/lib/types"

const names = (list: { name: string }[]) => new Intl.ListFormat(undefined, { type: "conjunction" }).format(list.map((d) => d.name))

function summary(p: PersonalAliasPolicy) {
  const usable = p.domains.filter((d) => d.available)
  if (!p.limitPerUser) return "People can’t create personal aliases. Existing ones keep working until deleted."
  if (!usable.length) return p.domains.length ? `Waiting for ${names(p.domains)} to be verified and active.` : "No domains are allowed, so people can’t create personal aliases."
  return `Up to ${p.limitPerUser} per person, ending in ${names(usable)}.`
}

export function PersonalAliasPolicyHeader({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const policy = useQuery(personalAliasPolicyQuery(orgId))
  const domains = useQuery(domainsQuery(orgId))
  const [editing, setEditing] = useState(false)
  const [limit, setLimit] = useState("0")
  const [ids, setIds] = useState<string[]>([])
  const [error, setError] = useState("")
  const save = useMutation({
    mutationFn: () => api(`/organizations/${orgId}/personal-alias-policy`, { method: "PUT", body: { limitPerUser: Number(limit), domainIds: ids } }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: personalAliasPolicyQuery(orgId).queryKey })
      setEditing(false)
      toast.success("Personal alias policy saved")
    },
  })
  const on = !!policy.data?.limitPerUser && policy.data.domains.some((d) => d.available)

  function open(p: PersonalAliasPolicy) {
    setLimit(String(p.limitPerUser))
    setIds(p.domains.map((d) => d.id))
    setError("")
    setEditing(true)
  }

  return (
    <>
      <SectionHeader
        title="Personal aliases"
        description="Addresses people create for themselves under Settings, such as a masked address for each site they sign up to. Each one forwards only to its owner’s mailbox. Turning one off or deleting it rejects mail sent to it."
        actions={
          policy.data && (
            <Button variant="secondary" onClick={() => open(policy.data)}>
              <Settings2 aria-hidden />
              Edit policy
            </Button>
          )
        }
      />
      <QueryView query={policy} skeleton={<Skeleton className="h-6 w-full max-w-md" />}>
        {(p) => (
          <p className="flex flex-wrap items-center gap-2 text-sm text-fg-secondary">
            <StatusBadge tone={on ? "success" : "neutral"}>{on ? "On" : "Off"}</StatusBadge>
            {summary(p)}
          </p>
        )}
      </QueryView>

      <FormDialog
        open={editing}
        onOpenChange={setEditing}
        title="Personal alias policy"
        description="Changes apply to new aliases. Existing ones keep working until their owner or an administrator deletes them."
        submitLabel="Save policy"
        pending={save.isPending}
        onSubmit={(e) => {
          e.preventDefault()
          if (!e.currentTarget.reportValidity()) return
          if (Number(limit) > 0 && !ids.length) setError("Allow at least one domain, or set the limit to 0.")
          else save.mutate()
        }}
      >
        <Field id="alias-limit" label="Aliases per person" hint="Counts every alias a person keeps, including ones that are off or expired. 0 stops new aliases.">
          <Input id="alias-limit" type="number" inputMode="numeric" min={0} max={2147483647} step={1} required className="w-32" value={limit} onChange={(e) => setLimit(e.target.value)} />
        </Field>
        <fieldset className="flex flex-col gap-3">
          <legend className="mb-2 text-label text-fg-secondary">Domains</legend>
          <QueryView query={domains} skeleton={<Skeleton className="h-10 w-full" />}>
            {(items) =>
              items.length ? (
                <ul className="flex flex-col rounded-xl border border-border">
                  {items.map((d) => (
                    <li key={d.id} className="flex items-center justify-between gap-4 border-b border-border p-4 last:border-b-0">
                      <div className="flex min-w-0 flex-col gap-1">
                        <Label htmlFor={`alias-domain-${d.id}`} className="break-all">
                          {d.name}
                        </Label>
                        {(!d.verified || !d.active) && <span className="text-sm text-fg-tertiary">{d.active ? "Not verified yet" : "Inactive"}. Aliases can’t be created on it until it is.</span>}
                      </div>
                      <Switch
                        id={`alias-domain-${d.id}`}
                        checked={ids.includes(d.id)}
                        onCheckedChange={(checked) => {
                          setIds(checked ? [...ids, d.id] : ids.filter((id) => id !== d.id))
                          setError("")
                        }}
                      />
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-sm text-fg-secondary">Add a domain under Domains first.</p>
              )
            }
          </QueryView>
          {error ? (
            <p role="alert" className="text-sm text-danger">
              {error}
            </p>
          ) : (
            <p className="text-sm text-fg-tertiary">Consider a separate domain, such as a subdomain, so personal aliases can’t take addresses you may later give to new staff.</p>
          )}
        </fieldset>
      </FormDialog>
    </>
  )
}

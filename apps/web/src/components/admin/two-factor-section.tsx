"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api } from "@/lib/api"
import type { TwoFactorPolicy } from "@/lib/types"

const policies: { value: TwoFactorPolicy; label: string; hint: string }[] = [
  { value: "off", label: "Nobody, it is optional", hint: "Anyone can turn on two-factor authentication in Settings, but nobody has to." },
  { value: "admins", label: "Owners and administrators", hint: "Owners and administrators of this organization must set it up before they can use Aether." },
  { value: "everyone", label: "Everyone", hint: "Every member must set it up before they can use Aether." },
]

export function TwoFactorSection({ orgId }: { orgId: string }) {
  const security = useQuery({ queryKey: ["org-security", orgId], queryFn: () => api<{ twoFactorPolicy: TwoFactorPolicy }>(`/organizations/${orgId}/security`) })

  return (
    <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <SectionHeader
        title="Two-factor authentication"
        description="Applies to people who sign in with an Aether password. People who sign in through an identity provider use that provider's two-factor settings. Someone without it can still sign in, but sees only the setup screen until they finish."
      />
      <QueryView query={security}>{(data) => <PolicyForm orgId={orgId} saved={data.twoFactorPolicy} />}</QueryView>
    </section>
  )
}

function PolicyForm({ orgId, saved }: { orgId: string; saved: TwoFactorPolicy }) {
  const qc = useQueryClient()
  const [policy, setPolicy] = useState(saved)
  const save = useMutation({
    mutationFn: () => api<{ twoFactorPolicy: TwoFactorPolicy }>(`/organizations/${orgId}/security`, { method: "PUT", body: { twoFactorPolicy: policy } }),
    onSuccess: (data) => {
      qc.setQueryData(["org-security", orgId], data)
      qc.invalidateQueries({ queryKey: ["session"] })
      toast.success("Two-factor requirement saved")
    },
  })

  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate()
      }}
    >
      <Field id="two-factor-policy" label="Required for" hint={policies.find((p) => p.value === policy)?.hint}>
        <Select value={policy} onValueChange={(v) => setPolicy(v as TwoFactorPolicy)}>
          <SelectTrigger id="two-factor-policy" className="w-full sm:max-w-sm">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {policies.map((p) => (
              <SelectItem key={p.value} value={p.value}>
                {p.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={policy === saved}>
        Save requirement
      </Button>
    </form>
  )
}

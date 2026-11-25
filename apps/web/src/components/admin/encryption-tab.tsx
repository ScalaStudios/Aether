"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import type { EncryptionLevel, EncryptionPolicy } from "@/lib/types"

const levels: { value: EncryptionLevel; label: string }[] = [
  { value: "off", label: "Off" },
  { value: "allow", label: "Allow" },
  { value: "require", label: "Require" },
]

export function EncryptionTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const policy = useQuery({
    queryKey: ["encryption", orgId],
    queryFn: () => api<EncryptionPolicy>(`/organizations/${orgId}/encryption`),
  })
  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Encryption"
        description="Off hides a method. Allow lets people choose it. Require means outgoing mail must use a method set to Require. When more than one is required, any one of them is enough. A password requirement applies only to recipients outside the verified domains below; encrypting to their key satisfies it too."
      />
      <QueryView query={policy}>
        {(current) => <Editor key={orgId} orgId={orgId} policy={current} onSaved={() => qc.invalidateQueries({ queryKey: ["encryption", orgId] })} />}
      </QueryView>
    </section>
  )
}

function Editor({ orgId, policy, onSaved }: { orgId: string; policy: EncryptionPolicy; onSaved: () => void }) {
  const [smime, setSmime] = useState(policy.smime)
  const [openpgp, setOpenpgp] = useState(policy.openpgp)
  const [password, setPassword] = useState(policy.password)
  const save = useMutation({
    mutationFn: () => api(`/organizations/${orgId}/encryption`, { method: "PUT", body: { smime, openpgp, password } }),
    onSuccess: () => {
      toast.success("Encryption settings saved")
      onSaved()
    },
  })
  return (
    <form
      className="flex max-w-3xl flex-col gap-6 rounded-xl border border-border bg-card p-6"
      onSubmit={(event) => {
        event.preventDefault()
        save.mutate()
      }}
    >
      <Choice label="S/MIME" value={smime} onChange={setSmime} hint="Certificates and RSA keys, imported from a PKCS#12 file." />
      <Choice label="OpenPGP" value={openpgp} onChange={setOpenpgp} hint="Keys people create in Aether or import. Autocrypt and Web Key Directory lookup are included." />
      <Choice label="Password-protected" value={password} onChange={setPassword} hint="A link in the email, opened with a password shared some other way. The message stays encrypted on Aether." />
      <p className="text-sm text-fg-tertiary">
        {policy.domains?.length ? `Verified domains: ${policy.domains.join(", ")}.` : "No verified domains yet. Every recipient counts as outside until one is verified."}
      </p>
      <div>
        <Button type="submit" loading={save.isPending}>Save</Button>
      </div>
    </form>
  )
}

function Choice({ label, value, onChange, hint }: { label: string; value: EncryptionLevel; onChange: (value: EncryptionLevel) => void; hint: string }) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="font-semibold">{label}</p>
        <SegmentedControl label={label} options={levels} value={value} onChange={onChange} />
      </div>
      <p className="text-sm text-fg-tertiary">{hint}</p>
    </div>
  )
}

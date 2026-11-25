"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useForm } from "react-hook-form"
import { toast } from "sonner"

import { SectionHeader } from "@/components/page-header"
import { QueryView, RowsSkeleton } from "@/components/states"
import { SwitchField } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import { organizationsQuery } from "@/lib/queries"
import type { SenderWarningSettings } from "@/lib/types"

export function SenderWarningsTab({ orgId }: { orgId: string }) {
  const settings = useQuery({ queryKey: ["sender-warnings", orgId], queryFn: () => api<SenderWarningSettings>(`/organizations/${orgId}/sender-warnings`) })

  return (
    <QueryView query={settings} skeleton={<RowsSkeleton rows={3} />}>
      {(data) => <SenderWarningsForm orgId={orgId} settings={data} />}
    </QueryView>
  )
}

function SenderWarningsForm({ orgId, settings }: { orgId: string; settings: SenderWarningSettings }) {
  const qc = useQueryClient()
  const orgName = useQuery({ ...organizationsQuery, select: (orgs) => orgs.find((o) => o.id === orgId)?.name }).data ?? "this organization"
  const form = useForm<SenderWarningSettings>({ defaultValues: settings })
  const save = useMutation({
    mutationFn: (values: SenderWarningSettings) => api<SenderWarningSettings>(`/organizations/${orgId}/sender-warnings`, { method: "PUT", body: values }),
    onSuccess: (saved) => {
      qc.setQueryData(["sender-warnings", orgId], saved)
      qc.invalidateQueries({ queryKey: ["message"] })
      form.reset(saved)
      toast.success("Sender warnings saved")
    },
  })

  return (
    <form noValidate className="flex flex-col gap-6 rounded-xl border border-border bg-card p-6" onSubmit={form.handleSubmit((v) => save.mutate(v))}>
      <SectionHeader
        title="Sender warnings"
        description={`Shown above messages in Aether Mail for every mailbox in ${orgName}. Other email apps don't show them. Sender verification results (SPF, DKIM and DMARC) stay available on every message.`}
      />
      <div className="flex flex-col gap-5">
        <SwitchField
          control={form.control}
          name="externalTag"
          id="sender-external-tag"
          label="Tag external senders"
          hint={`Show “External” next to the sender on mail from outside ${orgName}, including mail that uses your domains without coming from your mail server.`}
        />
        <SwitchField
          control={form.control}
          name="firstContact"
          id="sender-first-contact"
          label="Point out new senders"
          hint="Show “You don't often get email from this address” when an outside sender writes to a mailbox for the first time."
        />
        <SwitchField
          control={form.control}
          name="impersonation"
          id="sender-impersonation"
          label="Warn about impersonation"
          hint="Warn when an outside sender uses a colleague's name, puts one of your addresses in their name, writes from a domain that looks like yours, or uses your domain without passing sender verification."
        />
      </div>
      <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={!form.formState.isDirty}>
        Save sender warnings
      </Button>
    </form>
  )
}

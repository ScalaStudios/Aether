"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { CircleAlert, Lock } from "lucide-react"
import { toast } from "sonner"

import { LockNote } from "@/components/notice"
import { SectionHeader } from "@/components/page-header"
import { Spinner } from "@/components/spinner"
import { QueryView, RowsSkeleton } from "@/components/states"
import { SwitchRow } from "@/components/switch-field"
import { Button } from "@/components/ui/button"
import { useSession } from "@/hooks/use-session"
import { api, ApiError, errorMessage } from "@/lib/api"
import { featuresQuery } from "@/lib/queries"
import type { Feature } from "@/lib/types"

const features: { feature: Feature; label: string; hint: string }[] = [
  {
    feature: "calendar",
    label: "Calendar and contacts",
    hint: "When off, the Calendar and Contacts pages are hidden, invitations show as ordinary mail, and apps stop syncing over CalDAV and CardDAV. Calendars and contacts are kept.",
  },
  {
    feature: "dav",
    label: "CalDAV and CardDAV",
    hint: "When off, phone and desktop apps can no longer sync calendars and contacts. Both keep working in Aether on the web.",
  },
  {
    feature: "appPasswords",
    label: "App passwords",
    hint: "When off, app passwords stop working in mail, calendar and contacts apps and no new ones can be created. Existing ones are kept and work again when this is turned back on.",
  },
  {
    feature: "mailImport",
    label: "Mail import",
    hint: "When off, Import mail is removed from Settings and running imports are cancelled. Mail that was already copied stays in the mailbox.",
  },
  {
    feature: "externalForwarding",
    label: "Forwarding outside the organization",
    hint: "When off, mail rules stop forwarding to addresses outside this organization’s domains. The rules are kept and forward again when this is turned back on.",
  },
  {
    feature: "apiKeys",
    label: "API keys",
    hint: "When off, requests made with this organization’s API keys are refused. The keys are kept and work again when this is turned back on.",
  },
  {
    feature: "webhooks",
    label: "Webhooks",
    hint: "When off, nothing is sent to this organization’s webhooks, and events that happen meanwhile are not sent later. The webhooks are kept.",
  },
  {
    feature: "personalAliases",
    label: "Personal aliases",
    hint: "Aether has no personal aliases yet, so this has no effect. The setting is saved.",
  },
]

export function FeaturesTab({ orgId }: { orgId: string }) {
  const session = useSession().data
  const state = useQuery(featuresQuery(orgId))
  const platformAdmin = session?.user.isPlatformAdmin ?? false
  const owner = platformAdmin || !!session?.memberships.some((m) => m.orgId === orgId && m.role === "owner")

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Features"
        description="Turn off what this organization does not use. A feature that is off is hidden in Aether and its API stops answering for everyone in the organization. Its settings and data are kept, so turning it back on restores them."
      />
      {!owner && (
        <LockNote>Only owners of this organization can change features.</LockNote>
      )}
      <QueryView query={state} skeleton={<RowsSkeleton rows={8} />}>
        {(data) => (
          <ul className="flex flex-col divide-y divide-border rounded-xl border border-border">
            {features.map((f) => (
              <FeatureRow
                key={f.feature}
                orgId={orgId}
                {...f}
                on={data.features[f.feature]}
                editable={owner}
                locked={owner && !platformAdmin && data.platformAdminOnly.includes(f.feature)}
              />
            ))}
          </ul>
        )}
      </QueryView>
    </section>
  )
}

function FeatureRow({
  orgId,
  feature,
  label,
  hint,
  on,
  editable,
  locked,
}: {
  orgId: string
  feature: Feature
  label: string
  hint: string
  on: boolean
  editable: boolean
  locked: boolean
}) {
  const qc = useQueryClient()
  const { queryKey } = featuresQuery(orgId)
  const show = (value: boolean) => qc.setQueryData(queryKey, (old) => old && { ...old, features: { ...old.features, [feature]: value } })
  const save = useMutation({
    mutationFn: (value: boolean) => api(`/organizations/${orgId}/features`, { method: "PUT", body: { [feature]: value } }),
    meta: { silent: true },
    onMutate: async (value) => {
      await qc.cancelQueries({ queryKey })
      show(value)
    },
    onSuccess: (_data, value) => {
      show(value)
      toast.success(`${label} turned ${value ? "on" : "off"}`)
    },
    onError: (error, value) => {
      if (error instanceof ApiError && error.code === "rules_not_updated") return
      show(!value)
      toast.error(errorMessage(error))
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ["session"] }),
  })
  const rulesNotUpdated = save.error instanceof ApiError && save.error.code === "rules_not_updated"

  return (
    <li className="flex items-start justify-between gap-4 p-4">
      <SwitchRow id={`feature-${feature}`} label={label} hint={hint} checked={on} disabled={!editable || locked || save.isPending} onCheckedChange={save.mutate}>
        {locked && (
          <p className="flex gap-1 text-sm text-fg-secondary">
            <Lock aria-hidden className="size-3.5 shrink-0 translate-y-[3px] text-fg-tertiary" />
            Only a platform administrator can change this.
          </p>
        )}
        {rulesNotUpdated && (
          <div role="alert" className="mt-2 flex flex-col gap-3 rounded-lg border border-warning/40 bg-warning/12 p-4 text-sm sm:flex-row sm:items-center">
            <div className="flex flex-1 gap-3">
              <CircleAlert aria-hidden className="size-4 shrink-0 translate-y-0.5 text-warning" />
              <span>This is saved, but some mailboxes could not be updated on the mail server, so their rules still work as before.</span>
            </div>
            <Button variant="secondary" size="sm" className="self-start sm:self-auto" onClick={() => save.mutate(on)}>
              Try again
            </Button>
          </div>
        )}
      </SwitchRow>
      {save.isPending && <Spinner className="mt-1 shrink-0" />}
    </li>
  )
}

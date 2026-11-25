"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { TwoFactorSection } from "@/components/admin/two-factor-section"
import { SectionHeader } from "@/components/page-header"
import { QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { api } from "@/lib/api"
import type { OrgSecuritySettings } from "@/lib/types"

const card = "flex flex-col gap-4 rounded-xl border border-border bg-card p-6"

type EventToggleKey =
  | "appPasswordCreated"
  | "externalForwardingRule"
  | "delegateAdded"
  | "newDeviceSignIn"
  | "failedSignIns"

const eventOptions: { key: EventToggleKey; label: string; description: string }[] = [
  {
    key: "appPasswordCreated",
    label: "App password created",
    description: "Notify the mailbox owner when an app password or Apple device profile is created for their mailbox.",
  },
  {
    key: "externalForwardingRule",
    label: "External forwarding rule added",
    description: "Notify the mailbox owner when a mail rule starts forwarding messages outside this organization’s verified domains.",
  },
  {
    key: "delegateAdded",
    label: "Delegate access granted",
    description: "Notify the mailbox owner when someone is given delegate access to their mailbox or granted higher delegate permissions.",
  },
  {
    key: "newDeviceSignIn",
    label: "New device or country sign-in",
    description: "Notify the person when their account signs in from an unrecognized device or a new country.",
  },
  {
    key: "failedSignIns",
    label: "Repeated failed sign-ins",
    description: "Notify the person after repeated failed password sign-in attempts within 15 minutes.",
  },
]

export function SecurityTab({ orgId }: { orgId: string }) {
  const settings = useQuery({
    queryKey: ["org-security-notifications", orgId],
    queryFn: () => api<OrgSecuritySettings>(`/organizations/${orgId}/security-notifications`),
  })

  return (
    <div className="flex flex-col gap-6">
      <TwoFactorSection orgId={orgId} />
      <QueryView query={settings} skeleton={<RowsSkeleton rows={4} />}>
        {(data) => <SecurityForm key={JSON.stringify(data)} orgId={orgId} initial={data} />}
      </QueryView>
    </div>
  )
}

function SecurityForm({ orgId, initial }: { orgId: string; initial: OrgSecuritySettings }) {
  const qc = useQueryClient()
  const [values, setValues] = useState<OrgSecuritySettings>(initial)
  const [thresholdText, setThresholdText] = useState(String(initial.failedSignInThreshold))
  const [thresholdError, setThresholdError] = useState<string | undefined>()

  const save = useMutation({
    mutationFn: (next: OrgSecuritySettings) =>
      api<OrgSecuritySettings>(`/organizations/${orgId}/security-notifications`, {
        method: "PUT",
        body: next,
      }),
    onSuccess: (saved) => {
      qc.setQueryData(["org-security-notifications", orgId], saved)
      toast.success("Security notification settings saved")
    },
  })

  const parsedThreshold = Number(thresholdText)
  const thresholdValid = /^\d+$/.test(thresholdText.trim()) && parsedThreshold >= 3 && parsedThreshold <= 50
  const dirty =
    values.appPasswordCreated !== initial.appPasswordCreated ||
    values.externalForwardingRule !== initial.externalForwardingRule ||
    values.delegateAdded !== initial.delegateAdded ||
    values.newDeviceSignIn !== initial.newDeviceSignIn ||
    values.failedSignIns !== initial.failedSignIns ||
    values.notifyAdmins !== initial.notifyAdmins ||
    (thresholdValid ? parsedThreshold !== initial.failedSignInThreshold : thresholdText !== String(initial.failedSignInThreshold))

  function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!thresholdValid) {
      setThresholdError("Enter a whole number between 3 and 50.")
      return
    }
    setThresholdError(undefined)
    save.mutate({ ...values, failedSignInThreshold: parsedThreshold })
  }

  return (
    <form noValidate onSubmit={submit} className="flex flex-col gap-6">
      <section className={card}>
        <SectionHeader
          title="Account security notifications"
          description="Choose which events send an email alert and appear under Settings → Security for the affected person."
        />
        <div className="divide-y divide-border rounded-xl border border-border">
          {eventOptions.map((item) => {
            const id = `sec-toggle-${item.key}`
            return (
              <div key={item.key} className="flex flex-col gap-3 p-4">
                <div className="flex items-start justify-between gap-4">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor={id}>{item.label}</Label>
                    <p className="text-sm text-fg-tertiary">{item.description}</p>
                  </div>
                  <Switch
                    id={id}
                    checked={values[item.key]}
                    onCheckedChange={(checked) => setValues((prev) => ({ ...prev, [item.key]: checked }))}
                    className="mt-0.5 shrink-0"
                  />
                </div>
                {item.key === "failedSignIns" && values.failedSignIns && (
                  <div className="pt-1 sm:max-w-xs">
                    <Field
                      id="sec-failed-threshold"
                      label="Failed attempts before notifying"
                      hint="Number of failed sign-ins within 15 minutes (3 to 50)."
                      error={thresholdError}
                    >
                      <Input
                        id="sec-failed-threshold"
                        type="number"
                        min={3}
                        max={50}
                        inputMode="numeric"
                        value={thresholdText}
                        aria-invalid={!!thresholdError}
                        onChange={(e) => {
                          setThresholdText(e.target.value)
                          setThresholdError(undefined)
                        }}
                      />
                    </Field>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </section>

      <section className={card}>
        <SectionHeader
          title="Administrator alerts"
          description="Control whether organization owners and administrators also receive email copies of security notifications."
        />
        <div className="flex items-start justify-between gap-4 rounded-xl border border-border p-4">
          <div className="flex flex-col gap-1">
            <Label htmlFor="sec-notify-admins">Also notify organization administrators</Label>
            <p className="text-sm text-fg-tertiary">
              Send an email copy to active owners and administrators whenever an enabled event triggers. Administrators are always notified when someone reports suspicious activity.
            </p>
          </div>
          <Switch
            id="sec-notify-admins"
            checked={values.notifyAdmins}
            onCheckedChange={(checked) => setValues((prev) => ({ ...prev, notifyAdmins: checked }))}
            className="mt-0.5 shrink-0"
          />
        </div>
      </section>

      <div className="flex items-center justify-end">
        <Button type="submit" loading={save.isPending} disabled={!dirty}>
          Save security settings
        </Button>
      </div>
    </form>
  )
}

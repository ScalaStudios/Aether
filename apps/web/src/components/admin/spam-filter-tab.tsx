"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

import { SectionHeader } from "@/components/page-header"
import { QueryView, RowsSkeleton } from "@/components/states"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { api } from "@/lib/api"
import { organizationsQuery } from "@/lib/queries"
import type { Organization } from "@/lib/types"

export function SpamFilterTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const orgs = useQuery(organizationsQuery)
  const update = useMutation({
    mutationFn: (spamLearning: boolean) => api<Organization>(`/organizations/${orgId}`, { method: "PATCH", body: { spamLearning } }),
    onSuccess: (org) => {
      qc.setQueryData<Organization[]>(["organizations"], (list) => list?.map((o) => (o.id === org.id ? org : o)))
      toast.success(org.spamLearning ? "The spam filter now learns from Junk moves" : "The spam filter no longer learns from Junk moves")
    },
  })

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader title="Spam filter" description="Messages that score as spam are filed into Junk, unless they come from someone in the recipient's contacts or reply to a message the recipient sent." />
      <QueryView query={orgs} skeleton={<RowsSkeleton rows={1} />}>
        {(list) => {
          const org = list.find((o) => o.id === orgId)
          if (!org) return null
          return (
            <div className="flex items-start gap-3">
              <Switch
                id="spam-learning"
                checked={update.isPending ? update.variables : org.spamLearning}
                disabled={update.isPending}
                onCheckedChange={(checked) => update.mutate(checked)}
                className="mt-1"
              />
              <div className="flex flex-col gap-1">
                <Label htmlFor="spam-learning">Learn from Junk moves</Label>
                <p className="text-sm text-fg-tertiary">
                  Moving a message into Junk, or using Report spam, teaches the filter it is spam. Moving it out of Junk to any folder except Trash, or using Not spam, teaches it the message is wanted. This works from every mail app.
                </p>
              </div>
            </div>
          )
        }}
      </QueryView>
    </section>
  )
}

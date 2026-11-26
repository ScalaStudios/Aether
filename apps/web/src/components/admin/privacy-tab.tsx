"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { SectionHeader } from "@/components/page-header"
import { QueryView, RowsSkeleton } from "@/components/states"
import { api } from "@/lib/api"
import type { MailPrivacy } from "@/lib/types"

const options = [
  {
    value: "block" as const,
    title: "Block until someone asks",
    description: "Remote images stay hidden. A reader can show them for one message, or always for a sender.",
  },
  {
    value: "proxy" as const,
    title: "Load through the proxy",
    description: "Remote images load automatically. Aether fetches them, so the sender does not learn the reader's IP address.",
  },
]

export function PrivacyTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const privacy = useQuery({ queryKey: ["mail-privacy", orgId], queryFn: () => api<MailPrivacy>(`/organizations/${orgId}/mail-privacy`) })
  const save = useMutation({
    mutationFn: (remoteImages: MailPrivacy["remoteImages"]) => api<MailPrivacy>(`/organizations/${orgId}/mail-privacy`, { method: "PUT", body: { remoteImages } }),
    onSuccess: (saved) => {
      qc.setQueryData(["mail-privacy", orgId], saved)
      toast.success(saved.remoteImages === "proxy" ? "Remote images load through the proxy" : "Remote images stay blocked")
    },
  })
  const selected = save.isPending ? save.variables : privacy.data?.remoteImages

  return (
    <QueryView query={privacy} skeleton={<RowsSkeleton rows={3} />} isEmpty={() => false}>
      {() => (
        <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
          <SectionHeader
            title="Remote images"
            description="Known tracking images are removed either way. When an image is shown, the request comes from Aether, not from the reader's browser."
          />
          <fieldset className="flex flex-col gap-3" disabled={save.isPending}>
            <legend className="sr-only">Remote image default</legend>
            {options.map((option) => {
              const active = selected === option.value
              return (
                <label
                  key={option.value}
                  className={cn(
                    "flex min-h-11 cursor-pointer items-start gap-3 rounded-xl border px-4 py-3 transition-colors duration-160",
                    active ? "border-ember bg-ember/8" : "border-border hover:bg-muted"
                  )}
                >
                  <input
                    type="radio"
                    name="remote-images"
                    value={option.value}
                    checked={active}
                    onChange={() => {
                      if (option.value !== privacy.data?.remoteImages) save.mutate(option.value)
                    }}
                    className="mt-1 size-4 accent-ember"
                  />
                  <span className="flex min-w-0 flex-col gap-1">
                    <span className="font-semibold">{option.title}</span>
                    <span className="text-sm text-fg-secondary">{option.description}</span>
                  </span>
                </label>
              )
            })}
          </fieldset>
        </section>
      )}
    </QueryView>
  )
}

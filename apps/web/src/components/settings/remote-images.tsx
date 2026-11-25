"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ImageOff } from "lucide-react"
import { toast } from "sonner"

import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import { formatDate } from "@/lib/format"
import type { RemoteImageSender } from "@/lib/types"

export function RemoteImages() {
  const qc = useQueryClient()
  const senders = useQuery({ queryKey: ["remote-image-senders"], queryFn: () => api<RemoteImageSender[]>("/me/remote-image-senders") })
  const remove = useMutation({
    mutationFn: (address: string) => api(`/me/remote-image-senders/${encodeURIComponent(address)}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["remote-image-senders"] })
      qc.invalidateQueries({ queryKey: ["message"] })
      toast.success("Images from that sender are hidden again")
    },
  })

  return (
    <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <SectionHeader
        title="Remote images"
        description="Senders you always show images from. Aether loads those images through its proxy, so the sender does not learn your IP address. Your organization can also load images for everyone."
      />
      <QueryView
        query={senders}
        empty={
          <EmptyState
            icon={ImageOff}
            title="No senders yet"
            description="Open a message and choose Always show from this sender. Images stay hidden until you do, unless your organization loads them through the proxy."
            className="px-0 py-6"
          />
        }
      >
        {(rows) => (
          <ul className="flex flex-col">
            {rows.map((row) => (
              <li key={row.address} className="flex flex-col gap-3 border-t border-border py-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0">
                  <p className="break-all font-semibold">{row.address}</p>
                  <p className="text-sm text-fg-tertiary">Added {formatDate(row.createdAt)}</p>
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  className="max-md:min-h-11 sm:shrink-0"
                  loading={remove.isPending && remove.variables === row.address}
                  onClick={() => remove.mutate(row.address)}
                >
                  Remove
                </Button>
              </li>
            ))}
          </ul>
        )}
      </QueryView>
    </section>
  )
}

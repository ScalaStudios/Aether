"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { LogOut, MonitorSmartphone } from "lucide-react"
import { toast } from "sonner"

import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import type { ActiveSession } from "@/lib/types"

function describeAgent(ua: string) {
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /OPR\//.test(ua)
      ? "Opera"
      : /Firefox\/|FxiOS\//.test(ua)
        ? "Firefox"
        : /Chrome\/|CriOS\//.test(ua)
          ? "Chrome"
          : /Safari\//.test(ua)
            ? "Safari"
            : ""
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac OS X/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : ""
  return browser && os ? `${browser} on ${os}` : browser || os || ua || "Unknown device"
}

export function SessionList({ path, subject }: { path: string; subject?: string }) {
  const qc = useQueryClient()
  const sessions = useQuery({ queryKey: ["sessions", path], queryFn: () => api<ActiveSession[]>(path) })
  const end = useMutation({
    mutationFn: (id?: string) => api(id ? `${path}/${id}` : path, { method: "DELETE" }),
    onSuccess: (_d, id) => {
      toast.success(id ? "Session signed out" : subject ? `Signed ${subject} out everywhere` : "Signed out of your other sessions")
      qc.invalidateQueries({ queryKey: ["sessions", path] })
    },
  })
  const others = sessions.data?.filter((s) => !s.current) ?? []

  return (
    <div className="flex flex-col gap-4">
      <QueryView query={sessions} empty={<EmptyState icon={MonitorSmartphone} title="No active sessions" description={subject ? `${subject} is not signed in anywhere.` : undefined} />}>
        {(items) => (
          <ul className="flex flex-col rounded-xl border border-border bg-card">
            {items.map((s) => (
              <li key={s.id} className="flex items-center justify-between gap-3 border-b border-border p-4 last:border-b-0">
                <div className="flex min-w-0 flex-col gap-1">
                  <span className="flex flex-wrap items-center gap-2 font-medium">
                    {describeAgent(s.userAgent)}
                    {s.current && <StatusBadge tone="success">This browser</StatusBadge>}
                  </span>
                  <span className="text-sm break-words text-fg-tertiary">
                    {[s.ip, s.provider && `via ${s.provider}`, `last active ${formatDateTime(s.lastSeenAt)}`].filter(Boolean).join(" · ")}
                  </span>
                </div>
                {!s.current && (
                  <IconButton label={`Sign out ${describeAgent(s.userAgent)}, ${s.ip}`} size="icon-sm" disabled={end.isPending} onClick={() => end.mutate(s.id)}>
                    <LogOut />
                  </IconButton>
                )}
              </li>
            ))}
          </ul>
        )}
      </QueryView>
      {others.length > 0 && (
        <Button variant="secondary" className="self-start" loading={end.isPending && !end.variables} onClick={() => end.mutate(undefined)}>
          {subject ? "Sign out everywhere" : "Sign out other sessions"}
        </Button>
      )}
    </div>
  )
}

export function ActiveSessions() {
  return (
    <section className="flex flex-col gap-6">
      <SectionHeader title="Where you are signed in" description="Browsers signed in to your Aether account. Sign out any you do not recognize, then change your password." />
      <SessionList path="/me/sessions" />
    </section>
  )
}

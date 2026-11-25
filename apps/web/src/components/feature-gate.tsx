"use client"

import { ToggleLeft } from "lucide-react"
import Link from "next/link"

import { EmptyState } from "@/components/states"
import { Button } from "@/components/ui/button"
import { useHydrated } from "@/hooks/use-hydrated"
import { featureOff, useSession } from "@/hooks/use-session"
import type { Feature } from "@/lib/types"

export function FeatureGate({ feature, title, children }: { feature: Feature; title: string; children: React.ReactNode }) {
  const session = useSession().data
  const hydrated = useHydrated()
  if (!session || !hydrated) return null
  if (!featureOff(session, feature)) return children
  return (
    <EmptyState
      icon={ToggleLeft}
      title={title}
      description="Your organization has turned this off. Everything you saved is kept and comes back if an owner turns it on again."
      className="h-full"
      action={
        <Button variant="secondary" asChild>
          <Link href="/mail">Go to Mail</Link>
        </Button>
      }
    />
  )
}

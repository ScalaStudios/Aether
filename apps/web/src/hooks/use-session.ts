"use client"

import { useQuery } from "@tanstack/react-query"

import { api, setCsrfToken } from "@/lib/api"
import type { Session } from "@/lib/types"

export function useSession() {
  return useQuery({
    queryKey: ["session"],
    queryFn: async () => {
      const session = await api<Session>("/session")
      setCsrfToken(session.csrfToken)
      return session
    },
    staleTime: 5 * 60_000,
  })
}

export function canAdminister(session: Session) {
  return session.user.isPlatformAdmin || session.memberships.some((m) => m.role !== "member")
}

"use client"

import { useSyncExternalStore } from "react"

const noSubscribe = () => () => {}

export function useHydrated() {
  return useSyncExternalStore(noSubscribe, () => true, () => false)
}

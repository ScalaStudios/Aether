"use client"

import { useSyncExternalStore } from "react"

import { defaultListView, type ListView } from "@/lib/mail"

const storageKey = "aether.mailViews"
const listeners = new Set<() => void>()

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function useListView(id: string): [ListView, (view: ListView) => void] {
  const stored = useSyncExternalStore(subscribe, () => localStorage.getItem(storageKey), () => null)
  const view: ListView = JSON.parse(stored ?? "{}")[id] ?? defaultListView
  function setView(next: ListView) {
    localStorage.setItem(storageKey, JSON.stringify({ ...JSON.parse(localStorage.getItem(storageKey) ?? "{}"), [id]: next }))
    listeners.forEach((listener) => listener())
  }
  return [view, setView]
}

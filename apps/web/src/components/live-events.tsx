"use client"

import { useQuery, useQueryClient } from "@tanstack/react-query"
import { createContext, useContext, useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import { api } from "@/lib/api"
import { chimeForNewMail, unlockMailSound } from "@/lib/mail-sound"
import { myMailboxesQuery } from "@/lib/queries"
import type { Mailbox, NotificationSettings } from "@/lib/types"

const LiveContext = createContext(false)

export function useLiveConnected() {
  return useContext(LiveContext)
}

type LiveMessage = { uid: number; subject: string; from: string; address: string; seen: boolean; fresh?: boolean }
type LiveMail = { mailboxId?: string; folder?: string; kind?: string; messages?: LiveMessage[] }
type LiveCalendar = { title?: string; body?: string; url?: string; tag?: string }

function wantsMail(settings: NotificationSettings | undefined, mailboxId: string, folder: string) {
  if (!settings?.mailEnabled) return false
  const row = settings.folders.find((f) => f.mailboxId === mailboxId && f.folder === folder)
  if (row) return row.enabled
  const folded = settings.folders.find((f) => f.mailboxId === mailboxId && f.folder.toLowerCase() === folder.toLowerCase())
  if (folded) return folded.enabled
  return folder.toLowerCase() === "inbox"
}

function sentByMe(mailboxes: Mailbox[] | undefined, address: string) {
  const sender = address.toLowerCase()
  return !!mailboxes?.some((m) => m.access === "owner" && (m.address === sender || m.fromAddresses?.some((from) => from.address === sender)))
}

export function LiveEvents({ children }: { children: React.ReactNode }) {
  const qc = useQueryClient()
  useQuery({ queryKey: ["notifications"], queryFn: () => api<NotificationSettings>("/me/notifications") })
  useQuery(myMailboxesQuery)
  const [connected, setConnected] = useState(false)
  const hasPush = useRef(false)
  const pushKnown = useRef(false)

  useEffect(() => {
    if (!("serviceWorker" in navigator)) {
      pushKnown.current = true
      return
    }
    let stop = false
    navigator.serviceWorker.register("/sw.js").then(async (reg) => {
      const sub = await reg.pushManager.getSubscription()
      if (stop) return
      hasPush.current = !!sub
      pushKnown.current = true
    }).catch(() => {
      pushKnown.current = true
    })
    const onMessage = (event: MessageEvent) => {
      if (event.data?.type !== "aether-push") return
      qc.invalidateQueries({ queryKey: ["messages"] })
      qc.invalidateQueries({ queryKey: ["folders"] })
      qc.invalidateQueries({ queryKey: ["scheduled"] })
      qc.invalidateQueries({ queryKey: ["snoozed"] })
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
    }
    navigator.serviceWorker.addEventListener("message", onMessage)
    return () => {
      stop = true
      navigator.serviceWorker.removeEventListener("message", onMessage)
    }
  }, [qc])

  useEffect(() => {
    const source = new EventSource("/api/v1/events")
    window.addEventListener("pointerup", unlockMailSound)
    window.addEventListener("keydown", unlockMailSound)
    const refreshMail = () => {
      qc.invalidateQueries({ queryKey: ["messages"] })
      qc.invalidateQueries({ queryKey: ["folders"] })
      qc.invalidateQueries({ queryKey: ["scheduled"] })
      qc.invalidateQueries({ queryKey: ["snoozed"] })
    }
    const notify = (title: string, body: string, url: string, tag: string) => {
      if (document.visibilityState !== "hidden" || !pushKnown.current || hasPush.current || !("Notification" in window) || Notification.permission !== "granted") return
      const note = new Notification(title, { body, tag, icon: "/icon-192.png" })
      note.onclick = () => {
        window.focus()
        window.location.assign(url)
      }
    }
    source.onopen = () => {
      setConnected(true)
      refreshMail()
    }
    source.onerror = () => setConnected(false)
    source.addEventListener("mail", (event) => {
      refreshMail()
      let data: LiveMail
      try {
        data = JSON.parse((event as MessageEvent).data) as LiveMail
      } catch {
        return
      }
      const unseen = (data.messages ?? []).filter((m) => !m.seen)
      if (data.kind !== "new" || unseen.length === 0 || !data.mailboxId || !data.folder) return
      if (!wantsMail(qc.getQueryData<NotificationSettings>(["notifications"]), data.mailboxId, data.folder)) return
      const mine = qc.getQueryData(myMailboxesQuery.queryKey)
      if (mine && unseen.some((m) => m.fresh && !sentByMe(mine, m.address))) chimeForNewMail()
      const first = unseen[0]
      const title = unseen.length > 1 ? `${unseen.length} new messages` : "New mail"
      const body = [first.from, first.subject].filter(Boolean).join("\n")
      const url = `/mail?mailbox=${encodeURIComponent(data.mailboxId)}&folder=${encodeURIComponent(data.folder)}&message=${first.uid}`
      notify(title, body, url, `mail:${data.mailboxId}:${data.folder}`)
    })
    source.addEventListener("calendar", (event) => {
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
      let data: LiveCalendar
      try {
        data = JSON.parse((event as MessageEvent).data) as LiveCalendar
      } catch {
        return
      }
      const title = data.title || "Calendar reminder"
      const body = data.body || ""
      if (document.visibilityState === "visible") {
        toast(title, { description: body || undefined })
        return
      }
      const current = qc.getQueryData<NotificationSettings>(["notifications"])
      if (current && !current.calendarEnabled) return
      notify(title, body, data.url || "/calendar", data.tag || "calendar")
    })
    source.addEventListener("resync", () => {
      refreshMail()
      qc.invalidateQueries({ queryKey: ["calendar-events"] })
    })
    return () => {
      source.close()
      window.removeEventListener("pointerup", unlockMailSound)
      window.removeEventListener("keydown", unlockMailSound)
      setConnected(false)
    }
  }, [qc])

  return <LiveContext.Provider value={connected}>{children}</LiveContext.Provider>
}

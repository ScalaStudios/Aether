"use client"

import { useMutation, useQuery, useQueryClient, useQueries } from "@tanstack/react-query"
import { Bell, BellOff } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { SectionHeader } from "@/components/page-header"
import { QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { api, errorMessage } from "@/lib/api"
import { folderTree } from "@/lib/mail"
import { foldersQuery, myMailboxesQuery } from "@/lib/queries"
import type { Folder, FolderNotification, Mailbox, NotificationSettings } from "@/lib/types"

function inboxDefault(folder: Pick<Folder, "name" | "specialUse">) {
  return folder.specialUse === "inbox" || folder.name.toLowerCase() === "inbox"
}

function folderRows(mailboxes: Mailbox[], folders: Folder[][], saved: FolderNotification[]) {
  const chosen = new Map(saved.map((row) => [`${row.mailboxId}\n${row.folder}`, row.enabled]))
  return mailboxes.flatMap((mailbox, index) =>
    folderTree(folders[index] ?? []).map(({ folder, depth, label }) => {
      const saved = chosen.get(`${mailbox.id}\n${folder.name}`)
      return {
        mailboxId: mailbox.id,
        address: mailbox.address,
        folder: folder.name,
        label,
        depth,
        enabled: saved ?? inboxDefault(folder),
      }
    })
  )
}

function urlBase64ToBytes(value: string) {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (value.length % 4)) % 4)
  const raw = atob(padded)
  return Uint8Array.from(raw, (char) => char.charCodeAt(0))
}

function Toggle({
  id,
  label,
  hint,
  checked,
  disabled,
  onCheckedChange,
}: {
  id: string
  label: string
  hint?: string
  checked: boolean
  disabled?: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-start gap-3">
      <Switch id={id} checked={checked} disabled={disabled} onCheckedChange={onCheckedChange} className="mt-1" />
      <div className="flex min-w-0 flex-col gap-1">
        <Label htmlFor={id}>{label}</Label>
        {hint && <p className="text-sm text-fg-tertiary">{hint}</p>}
      </div>
    </div>
  )
}

export function Notifications() {
  const qc = useQueryClient()
  const settings = useQuery({ queryKey: ["notifications"], queryFn: () => api<NotificationSettings>("/me/notifications") })
  const mailboxes = useQuery(myMailboxesQuery)
  const boxes = mailboxes.data ?? []
  const folderQueries = useQueries({ queries: boxes.map((mailbox) => foldersQuery(mailbox.id)) })
  const foldersReady = boxes.length === 0 || folderQueries.every((query) => query.isSuccess)
  const [pushPending, setPushPending] = useState(false)
  const [permission, setPermission] = useState<NotificationPermission | "unsupported">(() =>
    typeof Notification === "undefined" ? "unsupported" : Notification.permission
  )

  const save = useMutation({
    mutationFn: (body: Pick<NotificationSettings, "mailEnabled" | "calendarEnabled" | "folders">) =>
      api<NotificationSettings>("/me/notifications", { method: "PUT", body }),
    onSuccess: (saved) => {
      qc.setQueryData(["notifications"], saved)
      toast.success("Notifications saved")
    },
  })

  async function setBrowser(on: boolean) {
    const current = settings.data
    if (!current?.pushConfigured || !("serviceWorker" in navigator) || !("PushManager" in window)) return
    setPushPending(true)
    try {
      const reg = await navigator.serviceWorker.register("/sw.js")
      await navigator.serviceWorker.ready
      const existing = await reg.pushManager.getSubscription()
      if (!on) {
        if (existing) {
          await api("/me/push-subscriptions", { method: "DELETE", body: { endpoint: existing.endpoint } })
          await existing.unsubscribe()
        }
        toast.success("Browser notifications are off")
      } else {
        const next = await Notification.requestPermission()
        setPermission(next)
        if (next !== "granted") {
          toast.error("The browser blocked notifications. Allow them for this site and try again.")
          return
        }
        const sub = existing ?? (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToBytes(current.vapidPublicKey) }))
        const json = sub.toJSON()
        await api("/me/push-subscriptions", {
          method: "POST",
          body: { endpoint: json.endpoint, keys: { p256dh: json.keys?.p256dh, auth: json.keys?.auth } },
        })
        toast.success("This browser will notify you")
      }
      qc.invalidateQueries({ queryKey: ["notifications"] })
    } catch (error) {
      toast.error(errorMessage(error))
    } finally {
      setPushPending(false)
    }
  }

  return (
    <section className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
      <SectionHeader
        title="Notifications"
        description="Aether Mail updates while this tab is open. Browser notifications cover new mail and calendar reminders when the tab is in the background. Inbox is included until you turn it off; other folders stay quiet until you turn them on."
      />
      <QueryView query={settings} skeleton={<RowsSkeleton rows={4} />}>
        {(data) => {
          const rows = foldersReady && mailboxes.isSuccess ? folderRows(boxes, folderQueries.map((query) => query.data ?? []), data.folders) : null
          const payload = (patch: Partial<Pick<NotificationSettings, "mailEnabled" | "calendarEnabled">> & { folders?: FolderNotification[] }) => ({
            mailEnabled: patch.mailEnabled ?? data.mailEnabled,
            calendarEnabled: patch.calendarEnabled ?? data.calendarEnabled,
            folders: patch.folders ?? rows?.map(({ mailboxId, folder, enabled }) => ({ mailboxId, folder, enabled })) ?? data.folders,
          })
          return (
            <div className="flex flex-col gap-6">
              <Toggle
                id="notify-mail"
                label="New mail"
                hint="Unread messages in the folders you choose below."
                checked={data.mailEnabled}
                disabled={save.isPending}
                onCheckedChange={(checked) => save.mutate(payload({ mailEnabled: checked }))}
              />
              <Toggle
                id="notify-calendar"
                label="Calendar reminders"
                hint="A reminder you set on an event. For an all-day event, it is at the start of that day."
                checked={data.calendarEnabled}
                disabled={save.isPending}
                onCheckedChange={(checked) => save.mutate(payload({ calendarEnabled: checked }))}
              />
              <div className="flex flex-col gap-3 border-t border-border pt-4">
                <h3 className="text-label text-fg-tertiary">Folders</h3>
                {!mailboxes.isSuccess || !foldersReady ? (
                  <RowsSkeleton rows={3} />
                ) : rows && rows.length > 0 ? (
                  <div className="flex flex-col gap-5">
                    {boxes.map((mailbox) => {
                      const mine = rows.filter((row) => row.mailboxId === mailbox.id)
                      if (mine.length === 0) return null
                      return (
                        <fieldset key={mailbox.id} className="flex flex-col gap-3" disabled={!data.mailEnabled || save.isPending}>
                          <legend className="text-sm font-medium break-all">{mailbox.address}</legend>
                          {mine.map((row) => (
                            <div key={`${row.mailboxId}/${row.folder}`} style={{ paddingLeft: row.depth * 16 }}>
                              <Toggle
                                id={`notify-${row.mailboxId}-${encodeURIComponent(row.folder)}`}
                                label={row.label}
                                checked={row.enabled}
                                disabled={!data.mailEnabled || save.isPending}
                                onCheckedChange={(checked) =>
                                  save.mutate(
                                    payload({
                                      folders: rows.map((item) =>
                                        item.mailboxId === row.mailboxId && item.folder === row.folder
                                          ? { mailboxId: item.mailboxId, folder: item.folder, enabled: checked }
                                          : { mailboxId: item.mailboxId, folder: item.folder, enabled: item.enabled }
                                      ),
                                    })
                                  )
                                }
                              />
                            </div>
                          ))}
                        </fieldset>
                      )
                    })}
                  </div>
                ) : (
                  <p className="text-sm text-fg-tertiary">You do not have a mailbox yet, so there are no folders to watch.</p>
                )}
              </div>
              <div className="flex flex-col gap-3 border-t border-border pt-4">
                <h3 className="text-label text-fg-tertiary">This browser</h3>
                {data.pushConfigured ? (
                  <>
                    <p className="text-sm text-fg-tertiary">
                      {data.browsers === 0
                        ? "No browser is registered for background notifications."
                        : data.browsers === 1
                          ? "1 browser receives background notifications."
                          : `${data.browsers} browsers receive background notifications.`}
                      {permission === "denied" ? " This browser is blocking them." : ""}
                    </p>
                    <div className="flex flex-wrap gap-2">
                      <Button type="button" variant="secondary" loading={pushPending} onClick={() => setBrowser(true)}>
                        <Bell aria-hidden />
                        Allow on this browser
                      </Button>
                      {data.browsers > 0 && (
                        <Button type="button" variant="quiet" loading={pushPending} onClick={() => setBrowser(false)}>
                          <BellOff aria-hidden />
                          Turn off on this browser
                        </Button>
                      )}
                    </div>
                  </>
                ) : (
                  <p className="text-sm text-fg-tertiary">Background notifications are not available on this server. Open tabs still update as mail arrives.</p>
                )}
              </div>
              <div className="flex flex-col gap-2 border-t border-border pt-4">
                <h3 className="text-label text-fg-tertiary">iPhone and iPad Mail</h3>
                {data.ios.configured ? (
                  <p className="text-sm text-fg-tertiary">
                    Apple push is configured on this server.
                    {data.ios.devices === 0
                      ? " No iOS Mail app has registered yet. The bundled Dovecot does not speak XAPPLEPUSHSERVICE, so iOS Mail cannot register a device on its own."
                      : data.ios.devices === 1
                        ? " 1 device is registered."
                        : ` ${data.ios.devices} devices are registered.`}
                  </p>
                ) : (
                  <p className="text-sm text-fg-tertiary">
                    iOS Mail push needs an Apple Push certificate on the server, set with AETHER_APNS_CERT_FILE and AETHER_APNS_KEY_FILE. The bundled Dovecot does not implement XAPPLEPUSHSERVICE, so the Mail app cannot register a device yet. Use Aether Mail, or a mail app that keeps an IMAP connection open.
                  </p>
                )}
              </div>
            </div>
          )
        }}
      </QueryView>
    </section>
  )
}

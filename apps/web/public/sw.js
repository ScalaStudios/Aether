self.addEventListener("push", (event) => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch {
    data = { title: "Aether", body: event.data ? event.data.text() : "" }
  }
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true })
      for (const client of windows) client.postMessage({ type: "aether-push", data })
      if (windows.some((client) => client.focused)) return
      const url = typeof data.url === "string" && data.url.startsWith("/") ? data.url : "/"
      await self.registration.showNotification(data.title || "Aether", {
        body: data.body || "",
        icon: "/icon-192.png",
        tag: data.tag || "aether",
        data: { url },
      })
    })()
  )
})

self.addEventListener("notificationclick", (event) => {
  event.notification.close()
  const target = new URL(event.notification.data?.url || "/", self.location.origin)
  if (target.origin !== self.location.origin) return
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true })
      for (const client of windows) {
        await client.focus()
        if ("navigate" in client) await client.navigate(target.href)
        return
      }
      await self.clients.openWindow(target.href)
    })()
  )
})

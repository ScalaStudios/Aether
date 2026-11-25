import type { Metadata } from "next"
import { Suspense } from "react"

import { AdminView } from "@/components/admin/admin-view"

export const metadata: Metadata = {
  title: "Administration",
  description: "Manage organizations, domains, mailboxes, aliases, members, mail flow rules, the audit log, alerts, identity providers and the mail queue.",
}

export default function AdminPage() {
  return (
    <Suspense>
      <AdminView />
    </Suspense>
  )
}

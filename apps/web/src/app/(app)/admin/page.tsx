import type { Metadata } from "next"

import { AdminView } from "@/components/admin/admin-view"

export const metadata: Metadata = {
  title: "Administration",
  description: "Manage organizations, domains, mailboxes, aliases, members, mail flow rules, the audit log and identity providers.",
}

export default function AdminPage() {
  return <AdminView />
}

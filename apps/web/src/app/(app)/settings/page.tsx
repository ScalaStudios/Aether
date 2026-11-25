import type { Metadata } from "next"
import { Suspense } from "react"

import { SettingsView } from "@/components/settings/settings-view"

export const metadata: Metadata = {
  title: "Settings",
  description: "Your Aether profile, password, two-factor authentication, sessions, mail rules, and the app passwords that connect mail clients over IMAP and SMTP.",
}

export default function SettingsPage() {
  return (
    <Suspense>
      <SettingsView />
    </Suspense>
  )
}

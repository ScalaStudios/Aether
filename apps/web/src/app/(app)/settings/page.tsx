import type { Metadata } from "next"

import { SettingsView } from "@/components/settings/settings-view"

export const metadata: Metadata = {
  title: "Settings",
  description: "Your Aether profile, mail rules, and the app passwords that connect mail clients over IMAP and SMTP.",
}

export default function SettingsPage() {
  return <SettingsView />
}

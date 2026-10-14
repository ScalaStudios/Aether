import type { Metadata } from "next"

import { MailView } from "@/components/mail/mail-view"

export const metadata: Metadata = {
  title: "Mail",
  description: "Read, search and send email from your Aether mailboxes.",
}

export default function MailPage() {
  return <MailView />
}

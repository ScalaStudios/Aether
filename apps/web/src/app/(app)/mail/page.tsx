import type { Metadata } from "next"
import { Suspense } from "react"

import { MailView } from "@/components/mail/mail-view"

export const metadata: Metadata = {
  title: "Mail",
  description: "Read, search and send email from your Aether mailboxes.",
}

export default function MailPage() {
  return (
    <Suspense>
      <MailView />
    </Suspense>
  )
}

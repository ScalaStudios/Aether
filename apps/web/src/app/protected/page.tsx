import type { Metadata } from "next"
import { Suspense } from "react"

import { ProtectedMessageView } from "@/components/mail/protected-message"

export const metadata: Metadata = {
  title: "Protected message",
  description: "Open a password-protected Aether message. The password never leaves your browser.",
}

export default function ProtectedPage() {
  return (
    <Suspense>
      <ProtectedMessageView />
    </Suspense>
  )
}

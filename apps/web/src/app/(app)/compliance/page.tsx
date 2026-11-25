import type { Metadata } from "next"

import { ComplianceView } from "@/components/compliance/compliance-view"

export const metadata: Metadata = {
  title: "Compliance",
  description: "Legal holds, searches across mailboxes, exports and journaling for the organizations you investigate.",
}

export default function CompliancePage() {
  return <ComplianceView />
}

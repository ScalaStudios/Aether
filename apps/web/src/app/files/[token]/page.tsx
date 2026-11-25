import type { Metadata } from "next"

import { DownloadView } from "@/components/files/download-view"

export const metadata: Metadata = {
  title: "Download",
  description: "Download a file that was sent as a link from Aether.",
}

export const instant = false

export default async function FileDownloadPage({ params }: { params: Promise<{ token: string }> }) {
  const { token } = await params
  return <DownloadView token={token} />
}

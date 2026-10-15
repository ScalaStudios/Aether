import type { Metadata } from "next"

import { CalendarView } from "@/components/calendar/calendar-view"

export const metadata: Metadata = {
  title: "Calendar",
  description: "Your Aether calendar by week or month, synced to phones and desktop apps over CalDAV.",
}

export default function CalendarPage() {
  return <CalendarView />
}

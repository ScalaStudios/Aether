import type { Metadata } from "next"

import { CalendarView } from "@/components/calendar/calendar-view"

export const metadata: Metadata = {
  title: "Calendar",
  description: "Your Aether calendar by week or month, with tasks, a public booking page, and calendars subscribed from a link.",
}

export default function CalendarPage() {
  return <CalendarView />
}

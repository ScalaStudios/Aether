import type { Metadata } from "next"
import { Suspense } from "react"

import { PublicBooking } from "@/components/booking/booking-page"
import { RowsSkeleton } from "@/components/states"

export const metadata: Metadata = {
  title: "Book a meeting",
  description: "Choose an open time. You'll get a calendar invitation by email.",
}

async function BookRoute({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params
  return <PublicBooking slug={slug} />
}

export default function BookPage({ params }: { params: Promise<{ slug: string }> }) {
  return (
    <Suspense
      fallback={
        <main className="mx-auto flex min-h-dvh w-full max-w-3xl flex-col gap-6 px-5 py-8 md:py-12">
          <RowsSkeleton rows={6} />
        </main>
      }
    >
      <BookRoute params={params} />
    </Suspense>
  )
}

import type { Metadata } from "next"
import Image from "next/image"
import { Suspense } from "react"

import { LoginForm } from "@/components/login-form"

export const metadata: Metadata = {
  title: "Sign in",
  description: "Sign in to Aether with your email and password or your organization's identity provider.",
}

export default function LoginPage() {
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-12 px-5 py-12">
      <div className="flex w-full max-w-[400px] flex-col gap-8">
        <h1 className="font-display text-[40px] leading-none font-extrabold tracking-tight">Aether</h1>
        <Suspense>
          <LoginForm />
        </Suspense>
      </div>
      <p className="flex items-center gap-2 text-sm text-fg-tertiary">
        <Image src="/brand/scala-symbol-gradient.svg" alt="" width={24} height={31} className="h-[31px] w-6 light:hidden" />
        <Image src="/brand/scala-symbol-ink.svg" alt="" width={24} height={31} className="hidden h-[31px] w-6 light:block" />
        A Scala Studios project
      </p>
    </main>
  )
}

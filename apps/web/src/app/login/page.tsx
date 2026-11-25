import type { Metadata } from "next"
import { Suspense } from "react"

import { BrandHeading } from "@/components/branding"
import { LoginFooter, LoginForm } from "@/components/login-form"

export const metadata: Metadata = {
  title: "Sign in",
  description: "Sign in to Aether with your email and password or your organization's identity provider.",
}

export default function LoginPage() {
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-12 px-5 py-12">
      <div className="flex w-full max-w-[400px] flex-col gap-8">
        <BrandHeading />
        <Suspense>
          <LoginForm />
        </Suspense>
      </div>
      <LoginFooter />
    </main>
  )
}

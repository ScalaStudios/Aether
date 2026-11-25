"use client"

import { MutationCache, QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ThemeProvider } from "next-themes"
import { Suspense, useState } from "react"
import { toast } from "sonner"

import { BrandingEffects } from "@/components/branding"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { ApiError, errorMessage } from "@/lib/api"
import { I18nProvider } from "@/lib/i18n"

export function Providers({ children }: { children: React.ReactNode }) {
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            retry: (count, error) => !(error instanceof ApiError && error.status >= 400 && error.status < 500) && count < 2,
          },
        },
        mutationCache: new MutationCache({
          onError: (error, _variables, _context, mutation) => {
            if (!mutation.meta?.silent) toast.error(errorMessage(error))
          },
        }),
      })
  )

  return (
    <ThemeProvider attribute="class" defaultTheme="dark" themes={["dark", "light"]} enableSystem={false} storageKey="aether-theme" disableTransitionOnChange>
      <QueryClientProvider client={client}>
        <I18nProvider>
          <Suspense>
            <BrandingEffects />
          </Suspense>
          <TooltipProvider delayDuration={400}>{children}</TooltipProvider>
          <Toaster position="bottom-right" />
        </I18nProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}

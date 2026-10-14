"use client"

import { MutationCache, QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ThemeProvider } from "next-themes"
import { useState } from "react"
import { toast } from "sonner"

import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { ApiError, errorMessage } from "@/lib/api"

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
        <TooltipProvider delayDuration={400}>{children}</TooltipProvider>
        <Toaster position="bottom-right" />
      </QueryClientProvider>
    </ThemeProvider>
  )
}

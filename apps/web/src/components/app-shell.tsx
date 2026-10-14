"use client"

import { useMutation } from "@tanstack/react-query"
import { LogOut, Mail, Settings, ShieldCheck, type LucideIcon } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useTheme } from "next-themes"
import { cn } from "cn"

import { ErrorState } from "@/components/states"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { canAdminister, useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import type { User } from "@/lib/types"

const nav: { href: string; label: string; icon: LucideIcon; admin?: boolean }[] = [
  { href: "/mail", label: "Mail", icon: Mail },
  { href: "/settings", label: "Settings", icon: Settings },
  { href: "/admin", label: "Administration", icon: ShieldCheck, admin: true },
]

const railClass =
  "order-last flex h-16 shrink-0 items-stretch justify-around border-t border-border bg-card pb-[env(safe-area-inset-bottom)] md:order-first md:h-full md:w-24 md:flex-col md:justify-start md:gap-1 md:border-t-0 md:border-r md:py-4"

export function AppShell({ children }: { children: React.ReactNode }) {
  const session = useSession()
  const pathname = usePathname()

  if (session.isError) {
    return (
      <div className="flex h-dvh items-center justify-center">
        <ErrorState error={session.error} onRetry={() => session.refetch()} />
      </div>
    )
  }

  const items = session.data ? nav.filter((item) => !item.admin || canAdminister(session.data)) : []

  return (
    <div className="flex h-dvh flex-col md:flex-row">
      <a
        href="#main"
        className="sr-only z-50 rounded-lg bg-ember px-4 py-2 font-semibold text-ink focus:not-sr-only focus:fixed focus:top-4 focus:left-4"
      >
        Skip to content
      </a>
      <nav aria-label="Primary" className={railClass}>
        <span className="hidden pb-4 text-center font-display text-[15px] font-extrabold tracking-tight md:block">Aether</span>
        {session.isPending
          ? Array.from({ length: 3 }, (_, i) => <Skeleton key={i} className="m-2 h-12 md:mx-4" />)
          : items.map((item) => {
              const active = pathname.startsWith(item.href)
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "relative flex min-h-11 flex-1 flex-col items-center justify-center gap-1 px-1 text-xs font-medium text-fg-tertiary transition-colors duration-160 hover:text-foreground md:flex-none md:py-3",
                    active &&
                      "text-foreground before:absolute before:inset-x-4 before:top-0 before:h-0.5 before:bg-ember md:before:inset-x-auto md:before:inset-y-2 md:before:left-0 md:before:h-auto md:before:w-0.5"
                  )}
                >
                  <item.icon aria-hidden className={cn("size-5", active && "text-ember")} />
                  <span className="max-w-full truncate">{item.label}</span>
                </Link>
              )
            })}
        <div className="flex flex-1 items-center justify-center md:flex-none md:mt-auto">
          {session.data ? <UserMenu user={session.data.user} /> : <Skeleton className="size-8 rounded-full" />}
        </div>
      </nav>
      <main id="main" className="min-h-0 min-w-0 flex-1 overflow-hidden">
        {children}
      </main>
    </div>
  )
}

function initials(user: User) {
  const source = user.displayName || user.email
  return source
    .split(/[\s@.]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase())
    .join("")
}

function UserMenu({ user }: { user: User }) {
  const { theme, setTheme } = useTheme()
  const logout = useMutation({
    mutationFn: () => api<{ redirect?: string }>("/auth/logout", { method: "POST" }),
    onSuccess: (data) => window.location.assign(data?.redirect || "/login"),
  })

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label="Account menu"
        className="flex size-11 cursor-pointer items-center justify-center rounded-full"
      >
        <span className="flex size-8 items-center justify-center rounded-full bg-muted text-xs font-semibold text-foreground">
          {initials(user)}
        </span>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="right" align="end" className="w-64">
        <DropdownMenuLabel className="flex flex-col gap-1 px-3 py-2">
          <span className="truncate text-base font-semibold text-foreground">{user.displayName || user.email}</span>
          <span className="truncate text-sm font-normal text-fg-tertiary">{user.email}</span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">Theme</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={theme} onValueChange={setTheme}>
          <DropdownMenuRadioItem value="dark">Dark</DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="light">Light</DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          disabled={logout.isPending}
          onSelect={(e) => {
            e.preventDefault()
            logout.mutate()
          }}
        >
          <LogOut aria-hidden />
          {logout.isPending ? "Signing out…" : "Sign out"}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

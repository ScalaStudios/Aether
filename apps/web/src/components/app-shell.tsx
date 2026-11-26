"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect } from "react"
import { BookUser, CalendarDays, Gavel, LogOut, Mail, Settings, ShieldCheck, type LucideIcon } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useTheme } from "next-themes"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

import { Avatar } from "@/components/avatar"
import { BrandMark } from "@/components/branding"
import { LiveEvents } from "@/components/live-events"
import { EmptyState, ErrorState } from "@/components/states"
import { Button } from "@/components/ui/button"
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
import { canAdminister, canInvestigate, featureOff, useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { lockKeys, rememberUser } from "@/lib/crypto/session"
import { LOCALE_LABELS, SUPPORTED_LOCALES, useI18n } from "@/lib/i18n"
import { plural } from "@/lib/format"
import { alertSummaryQuery } from "@/lib/queries"
import type { Feature, Session, User } from "@/lib/types"

const nav: { href: string; labelKey: string; icon: LucideIcon; admin?: boolean; compliance?: boolean; feature?: Feature }[] = [
  { href: "/mail", labelKey: "nav.mail", icon: Mail },
  { href: "/calendar", labelKey: "nav.calendar", icon: CalendarDays, feature: "calendar" },
  { href: "/contacts", labelKey: "nav.contacts", icon: BookUser, feature: "calendar" },
  { href: "/settings", labelKey: "nav.settings", icon: Settings },
  { href: "/admin", labelKey: "nav.administration", icon: ShieldCheck, admin: true },
  { href: "/compliance", labelKey: "nav.compliance", icon: Gavel, compliance: true },
]

const railClass =
  "order-last flex h-16 shrink-0 items-stretch justify-around border-t border-border bg-card pb-[env(safe-area-inset-bottom)] md:order-first md:h-full md:w-24 md:flex-col md:justify-start md:gap-1 md:border-t-0 md:border-r md:py-4"

export function AppShell({ children }: { children: React.ReactNode }) {
  const { t } = useI18n()
  const session = useSession()
  const pathname = usePathname()
  useEffect(() => {
    if (session.data) rememberUser(session.data.user.id)
  }, [session.data])
  const firing = useQuery({ ...alertSummaryQuery, enabled: !!session.data?.user.isPlatformAdmin }).data?.firing ?? 0

  if (session.isError) {
    return (
      <div className="flex h-dvh items-center justify-center">
        <ErrorState error={session.error} onRetry={() => session.refetch()} />
      </div>
    )
  }

  const items = session.data
    ? nav.filter((item) => (!item.admin || canAdminister(session.data)) && (!item.compliance || canInvestigate(session.data)) && !(item.feature && featureOff(session.data, item.feature)))
    : []

  return (
    <div className="flex h-dvh flex-col md:flex-row">
      <a
        href="#main"
        className="sr-only z-50 rounded-lg bg-ember px-4 py-2 font-semibold text-ink focus:not-sr-only focus:fixed focus:top-4 focus:left-4"
      >
        {t("nav.skipToContent")}
      </a>
      <nav aria-label={t("nav.primary")} className={railClass}>
        <BrandMark />
        {session.isPending
          ? Array.from({ length: 5 }, (_, i) => <Skeleton key={i} className="m-2 h-12 md:mx-4" />)
          : items.map((item) => {
              const active = pathname.startsWith(item.href)
              const alerting = item.admin && firing > 0
              return (
                <Link
                  key={item.href}
                  href={alerting ? "/admin?tab=alerts" : item.href}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "relative flex min-h-11 flex-1 flex-col items-center justify-center gap-1 px-1 text-xs font-medium text-fg-tertiary transition-colors duration-160 hover:text-foreground md:flex-none md:py-3",
                    active &&
                      "text-foreground before:absolute before:inset-x-4 before:top-0 before:h-0.5 before:bg-ember md:before:inset-x-auto md:before:inset-y-2 md:before:left-0 md:before:h-auto md:before:w-0.5"
                  )}
                >
                  <span className="relative">
                    <item.icon aria-hidden className={cn("size-5", active && "text-ember")} />
                    {alerting && <span aria-hidden className="absolute -top-1 -right-1 size-2 rounded-full bg-danger" />}
                  </span>
                  <span className="max-w-full truncate">{t(item.labelKey)}</span>
                  {alerting && <span className="sr-only">, {plural(firing, "alert")} firing</span>}
                </Link>
              )
            })}
        <div className="flex flex-1 items-center justify-center md:flex-none md:mt-auto">
          {session.data ? <UserMenu user={session.data.user} /> : <Skeleton className="size-8 rounded-full" />}
        </div>
      </nav>
      <main id="main" className="min-h-0 min-w-0 flex-1 overflow-hidden">
        {session.data?.twoFactorSetupRequired && !pathname.startsWith("/settings") ? (
          <EmptyState
            icon={ShieldCheck}
            title="Set up two-factor authentication"
            description="Your organization requires a second step when you sign in with your password. Set up an authenticator app or a security key to keep using Aether."
            className="h-full"
            action={
              <Button asChild>
                <Link href="/settings#security">Set up two-factor authentication</Link>
              </Button>
            }
          />
        ) : session.data ? (
          <LiveEvents>{children}</LiveEvents>
        ) : (
          children
        )}
      </main>
    </div>
  )
}

function UserMenu({ user }: { user: User }) {
  const { t, userLocale, automaticLocale } = useI18n()
  const { theme, setTheme } = useTheme()
  const client = useQueryClient()
  const updateLocale = useMutation({
    mutationFn: (locale: string) => api<User>("/me/profile", { method: "PUT", body: { locale } }),
    onSuccess: (updated) => {
      client.setQueryData<Session>(["session"], (prev) => (prev ? { ...prev, user: updated } : prev))
      toast.success(t("nav.languageSaved"))
    },
  })
  const logout = useMutation({
    mutationFn: () => api<{ redirect?: string }>("/auth/logout", { method: "POST" }),
    onSuccess: (data) => {
      lockKeys()
      window.location.assign(data?.redirect || "/login")
    },
  })

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={t("nav.accountMenu")}
        className="flex size-11 cursor-pointer items-center justify-center rounded-full"
      >
        <Avatar address={user.email} name={user.displayName} />
      </DropdownMenuTrigger>
      <DropdownMenuContent side="right" align="end" className="w-64">
        <DropdownMenuLabel className="flex flex-col gap-1 px-3 py-2">
          <span className="truncate text-base font-semibold text-foreground">{user.displayName || user.email}</span>
          <span className="truncate text-sm font-normal text-fg-tertiary">{user.email}</span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">{t("nav.theme")}</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={theme} onValueChange={setTheme}>
          <DropdownMenuRadioItem value="dark">{t("nav.dark")}</DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="light">{t("nav.light")}</DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">{t("nav.language")}</DropdownMenuLabel>
        <DropdownMenuRadioGroup
          value={userLocale || "auto"}
          onValueChange={(value) => updateLocale.mutate(value === "auto" ? "" : value)}
        >
          <DropdownMenuRadioItem value="auto">
            {t("common.automaticWithLocale", { language: LOCALE_LABELS[automaticLocale] })}
          </DropdownMenuRadioItem>
          {SUPPORTED_LOCALES.map((code) => (
            <DropdownMenuRadioItem key={code} value={code}>
              {LOCALE_LABELS[code]}
            </DropdownMenuRadioItem>
          ))}
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
          {logout.isPending ? t("nav.signingOut") : t("nav.signOut")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

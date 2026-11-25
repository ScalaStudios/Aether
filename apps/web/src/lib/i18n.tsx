"use client"

import { useQueryClient, type QueryClient } from "@tanstack/react-query"
import { createContext, useCallback, useContext, useEffect, useMemo, useState, useSyncExternalStore } from "react"

import type { AuthProviders, Branding, Session } from "@/lib/types"
import en from "@/messages/en.json"
import es from "@/messages/es.json"
import pt from "@/messages/pt.json"

export const DEFAULT_LOCALE = "en" as const
export const SUPPORTED_LOCALES = ["en", "pt", "es"] as const
export type Locale = (typeof SUPPORTED_LOCALES)[number]

export const LOCALE_LABELS: Record<Locale, string> = {
  en: "English",
  pt: "Português",
  es: "Español",
}

const STORAGE_KEY = "aether-locale"

type CatalogNode = string | { [key: string]: CatalogNode }

const catalogs: Record<Locale, CatalogNode> = { en, pt, es }

export type TranslationParams = Record<string, string | number>

export function normalizeLocale(raw?: string | null): Locale | "" {
  if (!raw) return ""
  const tag = raw.trim().replaceAll("_", "-").toLowerCase()
  if (!tag) return ""
  for (const supported of SUPPORTED_LOCALES) {
    if (tag === supported || tag.startsWith(`${supported}-`)) return supported
  }
  return ""
}

export function detectBrowserLocale(): Locale {
  if (typeof navigator === "undefined") return DEFAULT_LOCALE
  const candidates = navigator.languages?.length ? navigator.languages : [navigator.language]
  for (const candidate of candidates) {
    const normalized = normalizeLocale(candidate)
    if (normalized) return normalized
  }
  return DEFAULT_LOCALE
}

function readStoredGuestLocale(): Locale | "" {
  if (typeof window === "undefined") return ""
  try {
    return normalizeLocale(window.localStorage.getItem(STORAGE_KEY))
  } catch {
    return ""
  }
}

function lookup(catalog: CatalogNode, key: string): string | undefined {
  let node: CatalogNode | undefined = catalog
  for (const part of key.split(".")) {
    if (!node || typeof node === "string") return undefined
    node = node[part]
  }
  return typeof node === "string" ? node : undefined
}

function interpolate(template: string, params?: TranslationParams): string {
  if (!params) return template
  return template.replace(/\{(\w+)\}/g, (match, name: string) => (name in params ? String(params[name]) : match))
}

export function translate(locale: Locale, key: string, params?: TranslationParams): string {
  const template = lookup(catalogs[locale], key) ?? lookup(catalogs[DEFAULT_LOCALE], key) ?? key
  return interpolate(template, params)
}

const pluralRulesCache = new Map<Locale, Intl.PluralRules>()

function pluralRules(locale: Locale): Intl.PluralRules {
  let rules = pluralRulesCache.get(locale)
  if (!rules) {
    rules = new Intl.PluralRules(locale)
    pluralRulesCache.set(locale, rules)
  }
  return rules
}

function hasKey(locale: Locale, key: string): boolean {
  return (lookup(catalogs[locale], key) ?? lookup(catalogs[DEFAULT_LOCALE], key)) !== undefined
}

export function translatePlural(locale: Locale, key: string, count: number, params?: TranslationParams): string {
  let suffix = "other"
  if (count === 0) {
    suffix = hasKey(locale, `${key}_zero`) ? "zero" : "other"
  } else {
    const category = pluralRules(locale).select(count)
    suffix = hasKey(locale, `${key}_${category}`) ? category : "other"
  }
  return translate(locale, `${key}_${suffix}`, { count, ...params })
}

let activeLocale: Locale = DEFAULT_LOCALE

export function getActiveLocale(): Locale {
  return activeLocale
}

export function tActive(key: string, params?: TranslationParams): string {
  return translate(activeLocale, key, params)
}

export function tpActive(key: string, count: number, params?: TranslationParams): string {
  return translatePlural(activeLocale, key, count, params)
}

function resolveOrgLocale(session: Session | undefined, branding: Branding | null | undefined, authProviders: AuthProviders | undefined): Locale | "" {
  if (session) {
    const branded = branding?.orgId ? session.memberships.find((m) => m.orgId === branding.orgId) : undefined
    const brandedLocale = normalizeLocale(branded?.defaultLocale)
    if (brandedLocale) return brandedLocale
    for (const m of session.memberships) {
      const candidate = normalizeLocale(m.defaultLocale)
      if (candidate) return candidate
    }
    return ""
  }
  return normalizeLocale(authProviders?.defaultLocale)
}

function computeLocale(queryClient: QueryClient, browserLocale: Locale, guestLocale: Locale | ""): Locale {
  const session = queryClient.getQueryData<Session>(["session"])
  const branding = queryClient.getQueryData<Branding | null>(["branding"])
  const authProviders = queryClient.getQueryData<AuthProviders>(["auth-providers"])
  const userLocale = normalizeLocale(session?.user?.locale)
  const orgLocale = resolveOrgLocale(session, branding, authProviders)
  return session
    ? userLocale || orgLocale || browserLocale || DEFAULT_LOCALE
    : guestLocale || orgLocale || browserLocale || DEFAULT_LOCALE
}

const guestListeners = new Set<() => void>()

function subscribeGuestLocale(listener: () => void) {
  guestListeners.add(listener)
  const onStorage = (event: StorageEvent) => {
    if (event.key === STORAGE_KEY) listener()
  }
  window.addEventListener("storage", onStorage)
  return () => {
    guestListeners.delete(listener)
    window.removeEventListener("storage", onStorage)
  }
}

function subscribeBrowserLocale(listener: () => void) {
  window.addEventListener("languagechange", listener)
  return () => window.removeEventListener("languagechange", listener)
}

export interface I18nContextValue {
  locale: Locale
  userLocale: Locale | ""
  orgLocale: Locale | ""
  browserLocale: Locale
  automaticLocale: Locale
  guestLocale: Locale | ""
  setGuestLocale: (next: Locale | "") => void
  t: (key: string, params?: TranslationParams) => string
  tp: (key: string, count: number, params?: TranslationParams) => string
}

const I18nContext = createContext<I18nContextValue>({
  locale: DEFAULT_LOCALE,
  userLocale: "",
  orgLocale: "",
  browserLocale: DEFAULT_LOCALE,
  automaticLocale: DEFAULT_LOCALE,
  guestLocale: "",
  setGuestLocale: () => {},
  t: (key, params) => translate(DEFAULT_LOCALE, key, params),
  tp: (key, count, params) => translatePlural(DEFAULT_LOCALE, key, count, params),
})

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient()
  const browserLocale = useSyncExternalStore<Locale>(subscribeBrowserLocale, detectBrowserLocale, () => DEFAULT_LOCALE)
  const guestLocale = useSyncExternalStore<Locale | "">(subscribeGuestLocale, readStoredGuestLocale, () => "")
  const [, setTick] = useState(0)

  useEffect(() => {
    return queryClient.getQueryCache().subscribe((event) => {
      const first = event.query.queryKey[0]
      if (first === "session" || first === "branding" || first === "auth-providers") {
        activeLocale = computeLocale(queryClient, detectBrowserLocale(), readStoredGuestLocale())
        setTick((n) => n + 1)
      }
    })
  }, [queryClient])

  const session = queryClient.getQueryData<Session>(["session"])
  const branding = queryClient.getQueryData<Branding | null>(["branding"])
  const authProviders = queryClient.getQueryData<AuthProviders>(["auth-providers"])

  const userLocale = normalizeLocale(session?.user?.locale)
  const orgLocale = resolveOrgLocale(session, branding, authProviders)
  const automaticLocale: Locale = orgLocale || browserLocale || DEFAULT_LOCALE
  const locale: Locale = session
    ? userLocale || orgLocale || browserLocale || DEFAULT_LOCALE
    : guestLocale || orgLocale || browserLocale || DEFAULT_LOCALE

  useEffect(() => {
    activeLocale = locale
    document.documentElement.lang = locale
  }, [locale])

  const setGuestLocale = useCallback(
    (next: Locale | "") => {
      try {
        if (next) window.localStorage.setItem(STORAGE_KEY, next)
        else window.localStorage.removeItem(STORAGE_KEY)
      } catch {}
      activeLocale = computeLocale(queryClient, detectBrowserLocale(), next)
      for (const listener of guestListeners) listener()
    },
    [queryClient]
  )

  const t = useCallback((key: string, params?: TranslationParams) => translate(locale, key, params), [locale])
  const tp = useCallback((key: string, count: number, params?: TranslationParams) => translatePlural(locale, key, count, params), [locale])

  const value = useMemo<I18nContextValue>(
    () => ({ locale, userLocale, orgLocale, browserLocale, automaticLocale, guestLocale, setGuestLocale, t, tp }),
    [locale, userLocale, orgLocale, browserLocale, automaticLocale, guestLocale, setGuestLocale, t, tp]
  )

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

export function useI18n(): I18nContextValue {
  return useContext(I18nContext)
}

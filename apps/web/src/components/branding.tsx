"use client"

import { useQuery } from "@tanstack/react-query"
import { cn } from "cn"
import { usePathname } from "next/navigation"
import { useEffect, useSyncExternalStore } from "react"

import { Skeleton } from "@/components/ui/skeleton"
import { brandCSS, brandingCache } from "@/lib/branding"
import { brandingQuery } from "@/lib/queries"
import type { Branding } from "@/lib/types"

function headElement<K extends "style" | "link">(tag: K, id: string, wanted: boolean, update: (el: HTMLElementTagNameMap[K]) => void) {
  let el = document.getElementById(id) as HTMLElementTagNameMap[K] | null
  if (!wanted) return el?.remove()
  if (!el) {
    el = document.createElement(tag)
    el.id = id
    document.head.append(el)
  }
  update(el)
}

export function BrandingEffects() {
  const branding = useQuery(brandingQuery).data
  const admin = usePathname().startsWith("/admin")
  const name = branding?.name
  const favicon = branding?.favicon

  useEffect(() => {
    if (branding === undefined) return
    const css = branding ? brandCSS(branding.colors) : ""
    const href = branding?.customCssUrl ?? ""
    headElement("style", "aether-brand", !!css, (el) => (el.textContent = css))
    headElement("link", "aether-brand-css", !!href && !admin, (el) => Object.assign(el, { rel: "stylesheet", href }))
    if (branding) localStorage.setItem(brandingCache, JSON.stringify({ css, href }))
    else localStorage.removeItem(brandingCache)
  }, [branding, admin])

  useEffect(() => {
    const apply = () => {
      const title = name ? document.title.replace(/(^| · )Aether$/, (_, separator: string) => separator + name) : document.title
      if (title !== document.title) document.title = title
      for (const link of document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"], link[rel="apple-touch-icon"]')) {
        link.dataset.href ??= link.getAttribute("href") ?? ""
        link.dataset.type ??= link.type
        const target = favicon ?? link.dataset.href
        if (link.getAttribute("href") === target) continue
        link.href = target
        if (favicon) link.removeAttribute("type")
        else if (link.dataset.type) link.type = link.dataset.type
      }
    }
    apply()
    if (!name && !favicon) return
    const observer = new MutationObserver(apply)
    observer.observe(document.head, { childList: true, subtree: true, characterData: true })
    return () => observer.disconnect()
  }, [name, favicon])

  return null
}

function BrandLogo({ branding, className }: { branding: Branding; className: string }) {
  return (
    <>
      <img src={branding.logoDark ?? branding.logoLight ?? ""} alt={branding.name} className={cn("block object-contain light:hidden", className)} />
      <img src={branding.logoLight ?? branding.logoDark ?? ""} alt={branding.name} className={cn("hidden object-contain light:block", className)} />
    </>
  )
}

const noSubscribe = () => () => {}

function useBranding() {
  const hydrated = useSyncExternalStore(noSubscribe, () => true, () => false)
  const { data, isPending } = useQuery(brandingQuery)
  return { pending: !hydrated || isPending, b: data }
}

export function BrandMark() {
  const { pending, b } = useBranding()
  if (pending) return <Skeleton className="mx-auto mb-4 hidden h-6 w-14 md:block" />
  if (b && (b.logoLight || b.logoDark)) {
    return (
      <span className="hidden justify-center px-2 pb-4 md:flex">
        <BrandLogo branding={b} className="max-h-8 max-w-20" />
      </span>
    )
  }
  return <span className="hidden px-2 pb-4 text-center font-display text-[15px] font-extrabold tracking-tight break-words md:block">{b?.name ?? "Aether"}</span>
}

export function BrandHeading() {
  const { pending, b } = useBranding()
  if (pending) return <Skeleton className="h-10 w-48" />
  return (
    <h1 className="font-display text-[40px] leading-none font-extrabold tracking-tight">
      {b && (b.logoLight || b.logoDark) ? <BrandLogo branding={b} className="h-12 w-auto max-w-60" /> : (b?.name ?? "Aether")}
    </h1>
  )
}

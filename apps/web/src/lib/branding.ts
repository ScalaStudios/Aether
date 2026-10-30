import type { BrandColorKey, BrandColors, BrandTheme } from "@/lib/types"

export const brandThemes: BrandTheme[] = ["dark", "light"]

export const brandColorFields: { key: BrandColorKey; label: string; variables: string[] }[] = [
  { key: "accent", label: "Accent", variables: ["--ember", "--primary"] },
  { key: "accentHover", label: "Accent on hover", variables: ["--ember-hover"] },
  { key: "accentActive", label: "Accent when pressed", variables: ["--ember-active"] },
  { key: "background", label: "Background", variables: ["--background"] },
  { key: "surface", label: "Surface", variables: ["--card"] },
  { key: "border", label: "Border", variables: ["--border"] },
  { key: "text", label: "Text", variables: ["--foreground"] },
  { key: "link", label: "Links", variables: ["--link"] },
]

export const aetherColors: Record<BrandTheme, Record<BrandColorKey, string>> = {
  dark: { accent: "#fa7e26", accentHover: "#ff8f3d", accentActive: "#e06a18", background: "#111111", surface: "#1c1917", border: "#2a2724", text: "#ffffff", link: "#fdb07c" },
  light: { accent: "#fa7e26", accentHover: "#ff8f3d", accentActive: "#e06a18", background: "#fafaf9", surface: "#ffffff", border: "#e7e5e2", text: "#2a2724", link: "#b85110" },
}

const neutralVariables = {
  "--sunken": "oklch(from var(--background) calc(l - 0.03) c h)",
  "--muted": "color-mix(in oklab, var(--card), var(--foreground) 6%)",
  "--border-strong": "color-mix(in oklab, var(--border), var(--foreground) 15%)",
  "--fg-secondary": "color-mix(in oklab, var(--foreground), var(--background) 20%)",
  "--fg-tertiary": "color-mix(in oklab, var(--foreground), var(--background) 35%)",
}

function readableOn(hex: string) {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b > 0.19 ? "#111111" : "#ffffff"
}

export function brandVariables(colors: BrandColors[BrandTheme]) {
  const vars: Record<string, string> = {}
  for (const { key, variables } of brandColorFields) {
    const value = colors[key]
    if (value) for (const v of variables) vars[v] = value
  }
  if (colors.accent) vars["--ink"] = vars["--primary-foreground"] = readableOn(colors.accent)
  if (colors.background || colors.surface || colors.border || colors.text) Object.assign(vars, neutralVariables)
  return vars
}

export function brandCSS(colors: BrandColors) {
  return brandThemes
    .map((theme) => {
      const declarations = Object.entries(brandVariables(colors[theme])).map(([name, value]) => `${name}:${value}`)
      return declarations.length ? `${theme === "dark" ? ":root:not(.light)" : ":root.light"}{${declarations.join(";")}}` : ""
    })
    .join("")
}

export const brandingCache = "aether.branding"

export const brandingScript = `try{var b=JSON.parse(localStorage.getItem("${brandingCache}"));if(b){var s=document.createElement("style");s.id="aether-brand";s.textContent=b.css;document.head.append(s);if(b.href&&!location.pathname.startsWith("/admin")){var l=document.createElement("link");l.id="aether-brand-css";l.rel="stylesheet";l.href=b.href;document.head.append(l)}}}catch(e){}`

import { createCn } from "cn/config"

export const cn = createCn({
  extend: { classGroups: { "font-size": [{ text: ["h1", "h2", "h3", "label"] }] } },
})

export function setQueryParams(values: Record<string, string | null>, keep = true) {
  const params = new URLSearchParams(keep ? window.location.search : "")
  for (const [key, value] of Object.entries(values)) {
    if (value) params.set(key, value)
    else params.delete(key)
  }
  const search = params.toString()
  if (search !== window.location.search.slice(1)) window.history.replaceState(null, "", search ? `?${search}` : window.location.pathname)
}

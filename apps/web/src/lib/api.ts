import { getActiveLocale, tActive } from "@/lib/i18n"

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string
  ) {
    super(message)
  }
}

type Query = Record<string, string | number | undefined | null>

let csrfToken = ""

export function setCsrfToken(token: string) {
  csrfToken = token
}

export function apiUrl(path: string, query?: Query) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value !== undefined && value !== null && value !== "") params.set(key, String(value))
  }
  const search = params.toString()
  return `/api/v1${path}${search ? `?${search}` : ""}`
}

export async function api<T = void>(
  path: string,
  { method = "GET", body, query, blob }: { method?: string; body?: unknown; query?: Query; blob?: boolean } = {}
): Promise<T> {
  const raw = body instanceof Blob
  const headers: Record<string, string> = { Accept: "application/json", "Accept-Language": getActiveLocale() }
  if (body !== undefined) headers["Content-Type"] = raw ? body.type || "application/octet-stream" : "application/json"
  if (method !== "GET") headers["X-CSRF-Token"] = csrfToken

  let res: Response
  try {
    res = await fetch(apiUrl(path, query), {
      method,
      headers,
      body: body === undefined || raw ? body : JSON.stringify(body),
      credentials: "same-origin",
    })
  } catch {
    throw new ApiError(0, "network", tActive("common.networkError"))
  }

  if (res.status === 401 && path !== "/auth/login" && window.location.pathname !== "/login") {
    window.location.replace("/login")
  }

  if (!res.ok) {
    const data = await res.json().catch(() => null)
    if (data?.error?.code === "two_factor_setup_required" && window.location.pathname !== "/settings") window.location.replace("/settings#security")
    throw new ApiError(
      res.status,
      data?.error?.code ?? `http_${res.status}`,
      data?.error?.message ?? tActive("common.httpError", { status: res.status })
    )
  }

  if (blob) return (await res.blob()) as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

const limited: Record<string, string> = {
  mailbox_limit_reached: "mailbox",
  alias_limit_reached: "alias",
  list_limit_reached: "distribution list",
  domain_limit_reached: "domain",
}

export function limitMessage(error: unknown) {
  const noun = error instanceof ApiError ? limited[error.code] : undefined
  return noun && `This organization has reached its ${noun} limit. Delete one you no longer need, or ask a platform administrator to raise the limit under Quotas and limits.`
}

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : tActive("common.genericError")
}

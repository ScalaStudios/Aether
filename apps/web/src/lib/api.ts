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
  { method = "GET", body, query }: { method?: string; body?: unknown; query?: Query } = {}
): Promise<T> {
  const raw = body instanceof Blob
  const headers: Record<string, string> = { Accept: "application/json" }
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
    throw new ApiError(0, "network", "Aether could not reach the server. Check your connection and try again.")
  }

  if (res.status === 401 && path !== "/auth/login" && window.location.pathname !== "/login") {
    window.location.replace("/login")
  }

  if (!res.ok) {
    const data = await res.json().catch(() => null)
    throw new ApiError(
      res.status,
      data?.error?.code ?? `http_${res.status}`,
      data?.error?.message ?? `The server responded with status ${res.status}. Try again in a moment.`
    )
  }

  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Something went wrong. Try again."
}

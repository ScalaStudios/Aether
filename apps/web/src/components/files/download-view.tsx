"use client"

import { useQuery } from "@tanstack/react-query"
import { Download, FileDown } from "lucide-react"
import Image from "next/image"
import { useState } from "react"

import { Field } from "@/components/field"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ApiError, api, apiUrl } from "@/lib/api"
import { formatBytes, formatDateTime } from "@/lib/format"
import type { PublicFileLink } from "@/lib/types"

const tokenPattern = /^[A-Za-z0-9_-]{43}$/

export function DownloadView({ token }: { token: string }) {
  const valid = tokenPattern.test(token)
  const file = useQuery({
    queryKey: ["public-file", token],
    enabled: valid,
    retry: false,
    queryFn: () => api<PublicFileLink>(`/files/${token}`),
  })

  if (!valid) return <DownloadFrame title="This link is not valid" body="Check the address in the email, or ask the sender for a new one." />
  if (file.isPending) {
    return (
      <DownloadFrame title="Loading the file" body="Checking this download link.">
        <div aria-busy aria-label="Loading" className="h-10 animate-pulse rounded-lg bg-muted" />
      </DownloadFrame>
    )
  }
  if (file.isError) {
    const expired = file.error instanceof ApiError && (file.error.status === 410 || file.error.code === "file_link_expired")
    return <DownloadFrame title={expired ? "This link has expired" : "This link is not available"} body={file.error instanceof Error ? file.error.message : "The file could not be loaded."} />
  }

  return <ReadyFile token={token} file={file.data} />
}

function ReadyFile({ token, file }: { token: string; file: PublicFileLink }) {
  const [password, setPassword] = useState("")
  const [error, setError] = useState("")
  const [pending, setPending] = useState(false)
  const href = apiUrl(`/files/${token}/content`)

  async function downloadWithPassword() {
    setError("")
    if (!password) {
      setError("Enter the password.")
      return
    }
    setPending(true)
    try {
      const res = await fetch(href, {
        method: "POST",
        headers: { Accept: "application/octet-stream", "Content-Type": "application/json" },
        body: JSON.stringify({ password }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => null)
        throw new ApiError(res.status, data?.error?.code ?? `http_${res.status}`, data?.error?.message ?? "The file could not be downloaded.")
      }
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement("a")
      anchor.href = url
      anchor.download = file.filename
      document.body.append(anchor)
      anchor.click()
      anchor.remove()
      setTimeout(() => URL.revokeObjectURL(url), 60_000)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "The file could not be downloaded.")
    } finally {
      setPending(false)
    }
  }

  return (
    <DownloadFrame title={file.filename} body={`${formatBytes(file.sizeBytes)}${file.expiresAt ? ` · Expires ${formatDateTime(file.expiresAt)}` : ""}`}>
      {file.passwordRequired ? (
        <form
          className="flex flex-col gap-4"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            void downloadWithPassword()
          }}
        >
          <Field id="file-password" label="Password" hint="The sender set a password. It was not included in the email." error={error}>
            <Input id="file-password" type="password" autoComplete="current-password" value={password} aria-invalid={!!error} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <Button type="submit" loading={pending}>
            <Download aria-hidden />
            Download
          </Button>
        </form>
      ) : (
        <Button asChild>
          <a href={href} download={file.filename}>
            <Download aria-hidden />
            Download
          </a>
        </Button>
      )}
    </DownloadFrame>
  )
}

function DownloadFrame({ title, body, children }: { title: string; body: string; children?: React.ReactNode }) {
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-12 px-5 py-12">
      <div className="flex w-full max-w-[440px] flex-col gap-8">
        <div className="flex flex-col gap-3">
          <FileDown aria-hidden className="size-8 text-fg-tertiary" />
          <h1 className="text-h2 break-words">{title}</h1>
          <p className="text-fg-secondary">{body}</p>
        </div>
        {children}
      </div>
      <p className="flex items-center gap-2 text-sm text-fg-tertiary">
        <Image src="/brand/scala-symbol-gradient.svg" alt="" width={24} height={31} className="h-[31px] w-6 light:hidden" />
        <Image src="/brand/scala-symbol-ink.svg" alt="" width={24} height={31} className="hidden h-[31px] w-6 light:block" />
        A Scala Studios project
      </p>
    </main>
  )
}

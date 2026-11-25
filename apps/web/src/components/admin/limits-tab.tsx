"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { HardDrive, Link2 } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { OrganizationLimits } from "@/components/admin/organization-limits"
import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api } from "@/lib/api"
import { formatBytes, formatDateTime } from "@/lib/format"
import type { FileLinkRecord, TransferLimits } from "@/lib/types"

const card = "flex flex-col gap-4 rounded-xl border border-border bg-card p-6"
const MiB = 1024 * 1024
const GiB = 1024 * 1024 * 1024

function units(bytes: number, unit: number) {
  return String(Math.max(0, Math.round(bytes / unit)))
}

function toBytes(value: string, unit: number) {
  if (!/^\d+$/.test(value.trim())) return null
  const n = Number(value)
  const bytes = n * unit
  if (!Number.isSafeInteger(bytes)) return null
  return bytes
}

export function LimitsTab({ orgId }: { orgId: string }) {
  const limits = useQuery({ queryKey: ["transfer-limits", orgId], queryFn: () => api<TransferLimits>(`/organizations/${orgId}/transfer-limits`) })
  return (
    <div className="flex flex-col gap-6">
      <OrganizationLimits orgId={orgId} />
      <QueryView query={limits} skeleton={<RowsSkeleton rows={4} />}>
        {(data) => (
          <div className="flex flex-col gap-6">
            <LimitsForm orgId={orgId} limits={data} />
            <StoredFiles orgId={orgId} />
          </div>
        )}
      </QueryView>
    </div>
  )
}

function LimitsForm({ orgId, limits }: { orgId: string; limits: TransferLimits }) {
  const qc = useQueryClient()
  const [message, setMessage] = useState(units(limits.messageBytes, MiB))
  const [attachment, setAttachment] = useState(units(limits.attachmentBytes, MiB))
  const [threshold, setThreshold] = useState(units(limits.linkThresholdBytes, MiB))
  const [linkFile, setLinkFile] = useState(units(limits.linkFileBytes, MiB))
  const [storage, setStorage] = useState(units(limits.storageBytes, GiB))
  const parsed = {
    messageBytes: toBytes(message, MiB),
    attachmentBytes: toBytes(attachment, MiB),
    linkThresholdBytes: toBytes(threshold, MiB),
    linkFileBytes: toBytes(linkFile, MiB),
    storageBytes: toBytes(storage, GiB),
  }
  const unchanged = parsed.messageBytes === limits.messageBytes && parsed.attachmentBytes === limits.attachmentBytes && parsed.linkThresholdBytes === limits.linkThresholdBytes && parsed.linkFileBytes === limits.linkFileBytes && parsed.storageBytes === limits.storageBytes
  const save = useMutation({
    mutationFn: () => api<TransferLimits>(`/organizations/${orgId}/transfer-limits`, { method: "PUT", body: parsed }),
    onSuccess: (saved) => {
      qc.setQueryData(["transfer-limits", orgId], saved)
      toast.success("Mail limits saved")
    },
  })
  const used = Math.min(limits.usedBytes, limits.storageBytes || limits.usedBytes)
  const percent = limits.storageBytes ? Math.min(100, Math.round((limits.usedBytes / limits.storageBytes) * 100)) : limits.usedBytes ? 100 : 0

  return (
    <form
      noValidate
      className={card}
      onSubmit={(e) => {
        e.preventDefault()
        if (Object.values(parsed).some((n) => n === null)) return
        save.mutate()
      }}
    >
      <SectionHeader title="Message and file limits" description="Applies to mail sent from this organization. Message and attachment sizes stay within the platform's Postfix limit. Larger files go out as download links." />
      <div className="flex flex-col gap-2">
        <div className="flex items-baseline justify-between gap-3 text-sm">
          <span className="text-fg-secondary">Download storage</span>
          <span className="text-fg-tertiary">{formatBytes(limits.usedBytes)} of {formatBytes(limits.storageBytes)}</span>
        </div>
        <div role="progressbar" aria-valuemin={0} aria-valuemax={limits.storageBytes} aria-valuenow={used} aria-label="Download storage in use" className="h-2 overflow-hidden rounded-full bg-muted">
          <div className="h-full rounded-full bg-ember" style={{ width: `${percent}%` }} />
        </div>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <LimitField id="limit-message" label="Message size" unit="MB" value={message} max={units(limits.platform.messageBytes, MiB)} hint={`Platform maximum ${formatBytes(limits.platform.messageBytes)}. This is the finished message Postfix will accept.`} onChange={setMessage} />
        <LimitField id="limit-attachment" label="Attachment size" unit="MB" value={attachment} max={message || undefined} hint="Files at or under the threshold share this total. The rest are sent as links." onChange={setAttachment} />
        <LimitField id="limit-threshold" label="Download-link threshold" unit="MB" value={threshold} max={attachment || undefined} hint="A file larger than this is uploaded and sent as a link instead of an attachment." onChange={setThreshold} />
        <LimitField id="limit-link" label="Largest download link" unit="MB" value={linkFile} max={units(limits.platform.linkFileBytes, MiB)} hint={`Platform maximum ${formatBytes(limits.platform.linkFileBytes)}.`} onChange={setLinkFile} />
        <LimitField id="limit-storage" label="Download storage" unit="GB" value={storage} max={units(limits.platform.storageBytes, GiB)} hint={`Platform maximum ${formatBytes(limits.platform.storageBytes)}. Set 0 to turn download links off.`} onChange={setStorage} />
      </div>
      <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={unchanged || Object.values(parsed).some((n) => n === null)}>
        Save limits
      </Button>
    </form>
  )
}

function LimitField({ id, label, unit, value, max, hint, onChange }: { id: string; label: string; unit: string; value: string; max?: string; hint: string; onChange: (value: string) => void }) {
  const invalid = !/^\d+$/.test(value.trim())
  return (
    <Field id={id} label={`${label} (${unit})`} hint={hint} error={invalid ? "Enter a whole number." : undefined}>
      <Input id={id} inputMode="numeric" autoComplete="off" value={value} aria-invalid={invalid} max={max} onChange={(e) => onChange(e.target.value)} />
    </Field>
  )
}

function StoredFiles({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const files = useQuery({ queryKey: ["file-links", orgId], queryFn: () => api<FileLinkRecord[]>(`/organizations/${orgId}/file-links`) })
  const [removing, setRemoving] = useState<FileLinkRecord | null>(null)
  const remove = useMutation({
    mutationFn: (id: string) => api(`/organizations/${orgId}/file-links/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Download link removed")
      setRemoving(null)
      qc.invalidateQueries({ queryKey: ["file-links", orgId] })
      qc.invalidateQueries({ queryKey: ["transfer-limits", orgId] })
    },
  })

  return (
    <section className={card}>
      <SectionHeader title="Download links" description="Files stored for links sent from this organization. Removing a link makes its address stop working." />
      <QueryView
        query={files}
        skeleton={<RowsSkeleton rows={3} />}
        empty={<EmptyState icon={HardDrive} title="No download links" description="When someone sends a file as a link, it is listed here until it expires or you remove it." />}
      >
        {(items) => (
          <ul className="flex flex-col gap-2">
            {items.map((file) => (
              <li key={file.id} className="flex flex-col gap-3 rounded-lg border border-border p-4 sm:flex-row sm:items-center">
                <Link2 aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium">{file.filename}</p>
                  <p className="text-sm text-fg-tertiary">
                    {file.mailboxAddress} · {formatBytes(file.sizeBytes)} · {file.downloadCount} {file.downloadCount === 1 ? "download" : "downloads"}
                    {file.passwordProtected ? " · Password" : ""}
                    {file.expired ? " · Expired" : file.expiresAt ? ` · Expires ${formatDateTime(file.expiresAt)}` : " · No expiry"}
                    {!file.committed ? " · Not sent yet" : ""}
                  </p>
                </div>
                <Button type="button" variant="quiet" size="sm" onClick={() => setRemoving(file)}>
                  Remove
                </Button>
              </li>
            ))}
          </ul>
        )}
      </QueryView>
      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
        title="Remove this download link?"
        description={removing ? `${removing.filename} will be deleted. Anyone with the link will no longer be able to download it.` : ""}
        confirmLabel="Remove link"
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </section>
  )
}

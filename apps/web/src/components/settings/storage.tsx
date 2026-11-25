"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { FolderDialog } from "@/components/mail/folder-dialog"
import { SectionHeader } from "@/components/page-header"
import { MailboxPicker } from "@/components/settings/mail-rules"
import { QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { tierText, UsageBar, usagePercent } from "@/components/usage-bar"
import { api, errorMessage } from "@/lib/api"
import { formatBytes, formatPercent } from "@/lib/format"
import { folderIcons, folderLabel, storageTier } from "@/lib/mail"
import { foldersQuery, myMailboxesQuery } from "@/lib/queries"
import type { Folder, Mailbox } from "@/lib/types"

export function Storage() {
  const mailboxes = useQuery(myMailboxesQuery)
  const [chosen, setChosen] = useState<string | null>(null)
  const mailbox = mailboxes.data?.find((m) => m.id === chosen) ?? mailboxes.data?.[0]
  if (mailboxes.isSuccess && !mailbox) return null

  return (
    <section id="storage" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader
        title="Storage"
        description="How much space your mail takes up, folder by folder. When a mailbox is full, new mail to it is returned to its senders."
        actions={<MailboxPicker mailboxes={mailboxes.data} value={mailbox?.id ?? null} onChange={setChosen} />}
      />
      {mailbox ? <MailboxStorage key={mailbox.id} mailbox={mailbox} /> : <RowsSkeleton rows={4} />}
    </section>
  )
}

function MailboxStorage({ mailbox }: { mailbox: Mailbox }) {
  const qc = useQueryClient()
  const folders = useQuery(foldersQuery(mailbox.id))
  const [emptying, setEmptying] = useState<Folder | null>(null)
  const [error, setError] = useState("")
  const editable = mailbox.access !== "read"
  const empty = useMutation({
    mutationFn: (f: Folder) => api(`/mailboxes/${mailbox.id}/folders/empty`, { method: "POST", query: { folder: f.name } }),
    meta: { silent: true },
    onSuccess: (_d, f) => {
      toast.success(`Emptied ${folderLabel(f)}`)
      setEmptying(null)
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
    },
    onError: (e) => setError(errorMessage(e)),
  })

  return (
    <QueryView query={folders} skeleton={<RowsSkeleton rows={4} />}>
      {(list) => {
        const used = list.reduce((n, f) => n + f.size, 0)
        const quota = mailbox.quotaBytes
        const levels = mailbox.quotaWarningPercents ?? []
        const tier = storageTier(used, quota, levels)
        const rows = [...list].sort((a, b) => b.size - a.size)
        const summary = !quota
          ? "This mailbox has no storage limit."
          : tier === "full"
            ? "New mail is being returned to its senders. Empty Trash and Junk, or delete large messages, to make room."
            : `${formatBytes(quota - used)} free.${levels.length ? ` Aether emails this mailbox when it passes ${levels.map((l) => `${l}%`).join(" and ")}.` : ""}`
        return (
          <div className="flex flex-col gap-6">
            <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-6">
              <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                <p className="flex flex-wrap items-baseline gap-x-2">
                  <span className="text-h3 tabular-nums">{formatBytes(used)}</span>
                  <span className="text-fg-secondary tabular-nums">{quota ? `of ${formatBytes(quota)} used` : "used"}</span>
                </p>
                {quota > 0 && <span className={cn("text-sm font-semibold tabular-nums", tierText[tier])}>{usagePercent(used, quota)}%</span>}
              </div>
              {quota > 0 && <UsageBar used={used} quota={quota} tier={tier} className="h-2" />}
              <p className="text-sm text-fg-tertiary">{summary}</p>
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Folder</TableHead>
                  <TableHead className="text-right max-sm:hidden">Messages</TableHead>
                  <TableHead className="text-right">Size</TableHead>
                  <TableHead className="text-right max-sm:hidden">Share</TableHead>
                  {editable && (
                    <TableHead className="w-24">
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  )}
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((f) => {
                  const Icon = folderIcons[f.specialUse]
                  const permanent = f.specialUse === "trash" || f.specialUse === "junk"
                  return (
                    <TableRow key={f.name}>
                      <TableCell>
                        <span className="flex min-w-0 items-center gap-3">
                          <Icon aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
                          <span className="truncate font-medium">{folderLabel(f)}</span>
                        </span>
                      </TableCell>
                      <TableCell className="text-right text-fg-secondary tabular-nums max-sm:hidden">{f.total.toLocaleString()}</TableCell>
                      <TableCell className="text-right whitespace-nowrap tabular-nums">{formatBytes(f.size)}</TableCell>
                      <TableCell className="text-right text-fg-secondary tabular-nums max-sm:hidden">{formatPercent(f.size, used)}</TableCell>
                      {editable && (
                        <TableCell className="text-right">
                          {permanent && f.total > 0 && (
                            <Button
                              variant="quiet"
                              size="sm"
                              className="-my-2"
                              onClick={() => {
                                setError("")
                                setEmptying(f)
                              }}
                            >
                              Empty
                            </Button>
                          )}
                        </TableCell>
                      )}
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
            {emptying && (
              <FolderDialog
                action={{ kind: "empty", folder: emptying }}
                folders={list}
                pending={empty.isPending}
                error={error}
                onClose={() => setEmptying(null)}
                onSubmit={() => empty.mutate(emptying)}
              />
            )}
          </div>
        )
      }}
    </QueryView>
  )
}

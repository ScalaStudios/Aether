"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus, ShieldBan, Trash2 } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { IconButton } from "@/components/icon-button"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { MailboxPicker } from "@/components/settings/mail-rules"
import { EmptyState, QueryView } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, ApiError, errorMessage } from "@/lib/api"
import { myMailboxesQuery } from "@/lib/queries"
import type { SenderAction, SenderEntry } from "@/lib/types"

export const senderListQuery = (mailboxId: string) => ({
  queryKey: ["sender-lists", "mailbox", mailboxId],
  queryFn: () => api<SenderEntry[]>(`/mailboxes/${mailboxId}/sender-lists`),
})

export function SenderLists() {
  const mailboxes = useQuery({ ...myMailboxesQuery, select: (list) => list.filter((m) => m.access !== "read") })
  const [chosen, setChosen] = useState<string | null>(null)
  const mailboxId = chosen ?? mailboxes.data?.[0]?.id ?? null
  if (!mailboxId) return null

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Blocked and trusted senders"
        description="Mail from a blocked sender or domain is deleted as it arrives, before your rules and automatic replies run. Mail from a trusted sender always goes to your inbox instead of Junk, but is still scanned for viruses."
        actions={<MailboxPicker mailboxes={mailboxes.data} value={mailboxId} onChange={setChosen} />}
      />
      <MailboxSenders key={mailboxId} mailboxId={mailboxId} />
    </section>
  )
}

function MailboxSenders({ mailboxId }: { mailboxId: string }) {
  const qc = useQueryClient()
  const query = senderListQuery(mailboxId)
  const entries = useQuery(query)
  const [value, setValue] = useState("")
  const [action, setAction] = useState<SenderAction>("block")
  const [error, setError] = useState("")
  const saved = (list: SenderEntry[]) => qc.setQueryData(query.queryKey, list)

  const add = useMutation({
    mutationFn: () => api<SenderEntry[]>(`/mailboxes/${mailboxId}/sender-lists`, { method: "POST", body: { action, value } }),
    meta: { silent: true },
    onSuccess: (list) => {
      saved(list)
      toast.success(action === "block" ? `Blocked ${value.trim()}` : `Trusted ${value.trim()}`)
      setValue("")
    },
    onError: (e) => {
      if (e instanceof ApiError && e.status === 422) setError(e.message)
      else toast.error(errorMessage(e))
    },
  })
  const remove = useMutation({
    mutationFn: (e: SenderEntry) => api<SenderEntry[]>(`/mailboxes/${mailboxId}/sender-lists/${e.id}`, { method: "DELETE" }),
    onSuccess: (list, e) => {
      saved(list)
      toast.success(`Removed ${e.value}`)
    },
  })

  return (
    <div className="flex flex-col gap-4">
      <form
        noValidate
        className="flex flex-col gap-3 sm:flex-row sm:items-start"
        onSubmit={(e) => {
          e.preventDefault()
          if (!value.trim()) setError("Enter an email address or domain.")
          else add.mutate()
        }}
      >
        <div className="flex-1">
          <Field id="personal-sender" label="Sender" hint="An email address, or a domain such as example.com, which includes its subdomains." error={error}>
            <Input
              id="personal-sender"
              autoComplete="off"
              spellCheck={false}
              placeholder="name@example.com"
              value={value}
              aria-invalid={!!error}
              onChange={(e) => {
                setValue(e.target.value)
                setError("")
              }}
            />
          </Field>
        </div>
        <div className="flex items-center gap-3 sm:pt-7">
          <SegmentedControl
            label="List"
            options={[
              { value: "block", label: "Block" },
              { value: "allow", label: "Trust" },
            ]}
            value={action}
            onChange={setAction}
          />
          <Button type="submit" variant="secondary" loading={add.isPending}>
            <Plus aria-hidden />
            Add
          </Button>
        </div>
      </form>
      <QueryView query={entries} empty={<EmptyState icon={ShieldBan} title="No blocked or trusted senders" description="Add a sender here, or block one from a message in Aether Mail." />}>
        {(items) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Sender</TableHead>
                <TableHead>List</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="font-mono text-sm break-all">{e.kind === "domain" ? `@${e.value}` : e.value}</TableCell>
                  <TableCell>
                    <StatusBadge tone={e.action === "block" ? "danger" : "success"}>{e.action === "block" ? "Blocked" : "Trusted"}</StatusBadge>
                  </TableCell>
                  <TableCell>
                    <IconButton label={`Remove ${e.value}`} size="icon-sm" disabled={remove.isPending} onClick={() => remove.mutate(e)}>
                      <Trash2 />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>
    </div>
  )
}

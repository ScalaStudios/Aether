"use client"

import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { SearchX, Settings2, ShieldCheck } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { PreviewSkeleton, QuarantineMessage } from "@/components/mail/quarantine-pane"
import { SectionHeader } from "@/components/page-header"
import { SearchInput } from "@/components/search-input"
import { SegmentedControl } from "@/components/segmented-control"
import { EmptyState, ErrorState, QueryView, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { api } from "@/lib/api"
import { formatDate, formatListDate, plural } from "@/lib/format"
import { accessOptions, categoryOrder, digestOptions, quarantineCategories, risky } from "@/lib/quarantine"
import type { QuarantineAccess, QuarantineCategory, QuarantineItem, QuarantinePage, QuarantinePolicy, QuarantinePreview } from "@/lib/types"

type Status = "held" | "requested" | "released"
type Action = { action: "release" | "delete"; ids: string[]; risky: boolean }

function PolicyDialog({ orgId, open, onOpenChange }: { orgId: string; open: boolean; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient()
  const policy = useQuery({ queryKey: ["quarantine-policy", orgId], queryFn: () => api<QuarantinePolicy>(`/organizations/${orgId}/quarantine-policy`), enabled: open })
  const [draft, setDraft] = useState<QuarantinePolicy | null>(null)
  const [retention, setRetention] = useState<string | null>(null)
  const value = draft ?? policy.data
  const days = retention ?? String(value?.retentionDays ?? "")
  const daysValid = /^\d+$/.test(days) && Number(days) >= 1 && Number(days) <= 90

  const save = useMutation({
    mutationFn: (p: QuarantinePolicy) => api<QuarantinePolicy>(`/organizations/${orgId}/quarantine-policy`, { method: "PUT", body: p }),
    onSuccess: (saved) => {
      toast.success("Saved the quarantine policy")
      qc.setQueryData(["quarantine-policy", orgId], saved)
      qc.invalidateQueries({ queryKey: ["org-quarantine", orgId] })
      close(false)
    },
  })

  function close(next: boolean) {
    onOpenChange(next)
    if (!next) {
      setDraft(null)
      setRetention(null)
    }
  }

  return (
    <FormDialog
      open={open}
      onOpenChange={close}
      wide
      title="Quarantine policy"
      description="Who may see and release each kind of held mail, how long it is kept, and how often people hear about it."
      submitLabel="Save policy"
      pending={save.isPending}
      onSubmit={(e) => {
        e.preventDefault()
        if (value && daysValid) save.mutate({ ...value, retentionDays: Number(days) })
      }}
    >
      <QueryView query={policy} skeleton={<RowsSkeleton rows={4} />}>
        {() =>
          value && (
            <div className="flex flex-col gap-6">
              <ul aria-label="Who can release" className="flex flex-col divide-y divide-border rounded-xl border border-border">
                {categoryOrder.map((c) => (
                  <li key={c} className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:gap-6">
                    <div className="flex min-w-0 flex-col gap-1">
                      <Label htmlFor={`access-${c}`}>{quarantineCategories[c].label}</Label>
                      <p className="text-sm text-fg-tertiary">{quarantineCategories[c].hint}</p>
                    </div>
                    <Select value={value.access[c]} onValueChange={(a) => setDraft({ ...value, access: { ...value.access, [c]: a as QuarantineAccess } })}>
                      <SelectTrigger id={`access-${c}`} className="w-full shrink-0 sm:w-60">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {accessOptions.map((o) => (
                          <SelectItem key={o.value} value={o.value}>
                            {o.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </li>
                ))}
              </ul>
              <p className="text-sm text-fg-tertiary">
                Administrators can always see and release everything. People who may only request release see the message and ask; administrators decide under Requested.
              </p>
              <Field id="quarantine-retention" label="Keep held mail for" hint="1 to 90 days. After that, held mail is deleted for good." error={days && !daysValid ? "Enter a whole number of days from 1 to 90." : undefined}>
                <div className="flex items-center gap-3">
                  <Input
                    id="quarantine-retention"
                    inputMode="numeric"
                    className="w-24 tabular-nums"
                    aria-invalid={!daysValid}
                    value={days}
                    onChange={(e) => setRetention(e.target.value.trim())}
                  />
                  <span className="text-fg-secondary">days</span>
                </div>
              </Field>
              <div className="flex flex-col gap-2">
                <Label>Digest emails</Label>
                <div className="flex">
                  <SegmentedControl label="Digest emails" options={digestOptions} value={value.digest} onChange={(digest) => setDraft({ ...value, digest })} />
                </div>
                <p className="text-sm text-fg-tertiary">A summary of newly held mail, delivered to each mailbox that can see it. Releasing from a digest always needs sign-in.</p>
              </div>
            </div>
          )
        }
      </QueryView>
    </FormDialog>
  )
}

function StatusCell({ item }: { item: QuarantineItem }) {
  if (item.status === "requested") return <StatusBadge tone="info">Release requested</StatusBadge>
  if (item.status === "held") return <span className="text-sm text-fg-secondary">Held</span>
  return (
    <div className="flex flex-col gap-1">
      <StatusBadge tone="success">Released</StatusBadge>
      {item.releasedAt && (
        <span className="text-xs text-fg-tertiary">
          {formatDate(item.releasedAt)}
          {item.releasedBy && ` by ${item.releasedBy}`}
        </span>
      )}
    </div>
  )
}

export function QuarantineTab({ orgId }: { orgId: string }) {
  const qc = useQueryClient()
  const [status, setStatus] = useState<Status>("held")
  const [category, setCategory] = useState<QuarantineCategory | "all">("all")
  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 300)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [previewId, setPreviewId] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<Action | null>(null)
  const [policyOpen, setPolicyOpen] = useState(false)

  const list = useInfiniteQuery({
    queryKey: ["org-quarantine", orgId, status, category, q],
    queryFn: ({ pageParam }) =>
      api<QuarantinePage>(`/organizations/${orgId}/quarantine`, { query: { status, category: category === "all" ? undefined : category, q, before: pageParam } }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
    placeholderData: keepPreviousData,
  })
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  const requested = list.data?.pages[0]?.requested ?? 0
  const selectable = status === "released" ? [] : items.map((i) => i.id)
  const chosen = items.filter((i) => selected.has(i.id))

  const preview = useQuery({
    queryKey: ["org-quarantine-item", orgId, previewId],
    queryFn: () => api<QuarantinePreview>(`/organizations/${orgId}/quarantine/${previewId}`),
    enabled: previewId !== null,
  })

  const act = useMutation({
    mutationFn: ({ action, ids }: Action) => api<{ released?: number; failed?: number; deleted?: number }>(`/organizations/${orgId}/quarantine/${action}`, { method: "POST", body: { ids } }),
    onSuccess: (res, { action, ids }) => {
      if (action === "delete") toast.success(`Deleted ${plural(res.deleted ?? 0, "message")}`)
      else if (res.failed) toast.warning(`Released ${res.released} of ${ids.length}. ${plural(res.failed, "message")} could not be delivered; try again in a moment.`)
      else toast.success(`Released ${plural(res.released ?? 0, "message")} to their inboxes`)
      setSelected(new Set())
      setConfirm(null)
      if (previewId && ids.includes(previewId)) setPreviewId(null)
      qc.invalidateQueries({ queryKey: ["org-quarantine", orgId] })
      qc.invalidateQueries({ queryKey: ["org-quarantine-item", orgId] })
      qc.invalidateQueries({ queryKey: ["audit", orgId] })
    },
  })

  function filter(apply: () => void) {
    apply()
    setSelected(new Set())
  }

  function run(action: Action["action"], targets: QuarantineItem[]) {
    const next = { action, ids: targets.map((t) => t.id), risky: targets.some((t) => risky(t.category)) }
    if (action === "delete" || next.risky) setConfirm(next)
    else act.mutate(next)
  }

  function toggle(id: string, on: boolean) {
    const next = new Set(selected)
    if (on) next.add(id)
    else next.delete(id)
    setSelected(next)
  }

  const filtered = q !== "" || category !== "all"
  const allChosen = selectable.length > 0 && selectable.every((id) => selected.has(id))
  const empty = {
    held: { title: "Nothing is held", description: "Mail that looks like spam, phishing or malware appears here instead of being rejected, so nothing is lost to a false positive." },
    requested: { title: "No release requests", description: "When someone asks for a held message, it waits here for an administrator to release or delete it." },
    released: { title: "Nothing released yet", description: "Released messages stay listed here, with who released them, until they expire." },
  }[status]

  return (
    <section className="flex flex-col gap-6">
      <SectionHeader
        title="Quarantine"
        description="Mail held for this organization’s mailboxes. Every release and deletion is recorded in the audit log."
        actions={
          <Button variant="secondary" onClick={() => setPolicyOpen(true)}>
            <Settings2 aria-hidden />
            Policy
          </Button>
        }
      />
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center">
        <SearchInput
          className="lg:max-w-sm lg:flex-1"
          aria-label="Search the quarantine"
          placeholder="Search sender, subject or recipient"
          value={search}
          onChange={(e) => filter(() => setSearch(e.target.value))}
        />
        <Select value={category} onValueChange={(v) => filter(() => setCategory(v as QuarantineCategory | "all"))}>
          <SelectTrigger aria-label="Category" className="w-full lg:w-56">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All categories</SelectItem>
            {categoryOrder.map((c) => (
              <SelectItem key={c} value={c}>
                {quarantineCategories[c].label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="flex lg:ml-auto">
          <SegmentedControl
            label="Status"
            options={[
              { value: "held" as const, label: "Held" },
              { value: "requested" as const, label: requested ? `Requested (${requested})` : "Requested" },
              { value: "released" as const, label: "Released" },
            ]}
            value={status}
            onChange={(v) => filter(() => setStatus(v))}
          />
        </div>
      </div>
      {chosen.length > 0 && (
        <div role="region" aria-label="Selected messages" className="animate-content flex flex-wrap items-center gap-3 rounded-xl border border-border bg-card px-4 py-3">
          <span className="text-sm font-semibold tabular-nums">{chosen.length} selected</span>
          <div className="ml-auto flex flex-wrap gap-2">
            <Button size="sm" loading={act.isPending && act.variables?.action === "release" && !confirm} onClick={() => run("release", chosen)}>
              Release
            </Button>
            <Button size="sm" variant="secondary" onClick={() => run("delete", chosen)}>
              Delete
            </Button>
            <Button size="sm" variant="quiet" onClick={() => setSelected(new Set())}>
              Clear selection
            </Button>
          </div>
        </div>
      )}
      {list.isPending ? (
        <RowsSkeleton rows={5} />
      ) : list.isError ? (
        <ErrorState error={list.error} onRetry={() => list.refetch()} />
      ) : items.length === 0 ? (
        filtered ? (
          <EmptyState
            icon={SearchX}
            title="No matching messages"
            description="Nothing in this view matches the search or category."
            action={
              <Button
                variant="secondary"
                onClick={() =>
                  filter(() => {
                    setSearch("")
                    setCategory("all")
                  })
                }
              >
                Clear filters
              </Button>
            }
          />
        ) : (
          <EmptyState icon={ShieldCheck} title={empty.title} description={empty.description} />
        )
      ) : (
        <div className="animate-content flex flex-col gap-4">
          <Table>
            <TableHeader>
              <TableRow>
                {status !== "released" && (
                  <TableHead className="w-10">
                    <Checkbox
                      aria-label="Select every message shown"
                      checked={allChosen ? true : chosen.length > 0 ? "indeterminate" : false}
                      onCheckedChange={(v) => setSelected(v === true ? new Set(selectable) : new Set())}
                    />
                  </TableHead>
                )}
                <TableHead>Received</TableHead>
                <TableHead>Recipient</TableHead>
                <TableHead>From</TableHead>
                <TableHead>Subject</TableHead>
                <TableHead>Reason</TableHead>
                <TableHead>Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((it) => {
                const c = quarantineCategories[it.category]
                return (
                  <TableRow key={it.id} data-state={selected.has(it.id) ? "selected" : undefined}>
                    {status !== "released" && (
                      <TableCell>
                        <Checkbox aria-label={`Select ${it.subject || "message without subject"}`} checked={selected.has(it.id)} onCheckedChange={(v) => toggle(it.id, v === true)} />
                      </TableCell>
                    )}
                    <TableCell className="text-sm text-fg-secondary tabular-nums">
                      <time dateTime={it.receivedAt}>{formatListDate(it.receivedAt)}</time>
                    </TableCell>
                    <TableCell>
                      <div className="flex max-w-56 min-w-0 flex-col">
                        <span className="truncate font-mono text-sm">{it.mailbox}</span>
                        {it.recipient !== it.mailbox && <span className="truncate text-xs text-fg-tertiary">via {it.recipient}</span>}
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className="flex max-w-56 min-w-0 flex-col">
                        <span className="truncate">{it.from.name || it.from.address || "Unknown sender"}</span>
                        {it.from.name && <span className="truncate font-mono text-sm text-fg-tertiary">{it.from.address}</span>}
                      </div>
                    </TableCell>
                    <TableCell>
                      <button
                        type="button"
                        onClick={() => setPreviewId(it.id)}
                        className="block max-w-80 cursor-pointer truncate text-left font-medium underline-offset-3 transition-colors duration-160 hover:text-link hover:underline"
                      >
                        {it.subject || "(no subject)"}
                      </button>
                    </TableCell>
                    <TableCell>
                      <StatusBadge tone={c.tone}>{c.label}</StatusBadge>
                    </TableCell>
                    <TableCell>
                      <StatusCell item={it} />
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
          {list.hasNextPage && (
            <Button variant="secondary" className="self-center" loading={list.isFetchingNextPage} onClick={() => list.fetchNextPage()}>
              Load older messages
            </Button>
          )}
        </div>
      )}

      <Sheet open={previewId !== null} onOpenChange={(open) => !open && setPreviewId(null)}>
        <SheetContent side="right" className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[720px]">
          <SheetHeader className="border-b border-border p-6 pr-14">
            <SheetTitle>Held message</SheetTitle>
            <SheetDescription>{preview.data ? `For ${preview.data.mailbox}` : "Loading…"}</SheetDescription>
          </SheetHeader>
          {preview.isPending ? (
            <PreviewSkeleton />
          ) : preview.isError ? (
            <ErrorState error={preview.error} onRetry={() => preview.refetch()} />
          ) : (
            <QuarantineMessage item={preview.data} showMailbox />
          )}
          {preview.data && preview.data.status !== "released" && (
            <SheetFooter className="flex-row justify-end gap-3 border-t border-border p-4">
              <Button variant="secondary" onClick={() => preview.data && run("delete", [preview.data])}>
                Delete
              </Button>
              <Button loading={act.isPending && act.variables?.action === "release" && !confirm} onClick={() => preview.data && run("release", [preview.data])}>
                Release to Inbox
              </Button>
            </SheetFooter>
          )}
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={
          confirm?.action === "delete"
            ? `Delete ${confirm.ids.length === 1 ? "this message" : plural(confirm.ids.length, "message")}?`
            : `Release ${confirm?.ids.length === 1 ? "this message" : plural(confirm?.ids.length ?? 0, "message")}?`
        }
        description={
          confirm?.action === "delete"
            ? "Deleted messages are gone for good and cannot be released later. Senders are not told."
            : "The selection includes phishing or malware. Released mail goes straight to each recipient’s inbox, with its links and attachments working."
        }
        confirmLabel={confirm?.action === "delete" ? "Delete" : "Release anyway"}
        pending={act.isPending}
        onConfirm={() => confirm && act.mutate(confirm)}
      />
      <PolicyDialog orgId={orgId} open={policyOpen} onOpenChange={setPolicyOpen} />
    </section>
  )
}

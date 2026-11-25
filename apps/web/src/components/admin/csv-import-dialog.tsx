"use client"

import { useMutation } from "@tanstack/react-query"
import { Download, FileSpreadsheet, RotateCcw, Upload } from "lucide-react"
import { useRef, useState } from "react"
import { toast } from "sonner"

import { SegmentedControl } from "@/components/segmented-control"
import { RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, errorMessage } from "@/lib/api"
import { csvHeaders, downloadCsv, failedRowsCsv, MAX_CSV_BYTES, parseImportCsv, type ParsedCsvRow, templateCsv } from "@/lib/csv"
import { formatBytes, plural } from "@/lib/format"
import type { AcceptFrom, BulkOperation, BulkOperationType, BulkResponse } from "@/lib/types"

type Phase = "upload" | "preview" | "report"
type RowState = "pending" | "valid" | "created" | "failed"
type Filter = "all" | "ok" | "failed"

interface RowStatus {
  row: ParsedCsvRow
  state: RowState
  address: string
  error?: string
}

const meta: Record<
  BulkOperationType,
  {
    title: string
    noun: string
    pluralNoun: string
    templateFile: string
    failedFile: string
    hint: string
  }
> = {
  mailbox: {
    title: "Import mailboxes from CSV",
    noun: "mailbox",
    pluralNoun: "mailboxes",
    templateFile: "mailboxes-template.csv",
    failedFile: "mailboxes-failed.csv",
    hint: "Columns: address, display_name, kind (user or shared), quota_mb, password (at least 12 characters, or empty for sign in with provider), active.",
  },
  alias: {
    title: "Import aliases from CSV",
    noun: "alias",
    pluralNoun: "aliases",
    templateFile: "aliases-template.csv",
    failedFile: "aliases-failed.csv",
    hint: "Columns: address (use @domain for a catch-all), destinations (separated by semicolons), active.",
  },
  list: {
    title: "Import distribution lists from CSV",
    noun: "distribution list",
    pluralNoun: "distribution lists",
    templateFile: "distribution-lists-template.csv",
    failedFile: "distribution-lists-failed.csv",
    hint: "Columns: address, name, members (separated by semicolons), accept_from (anyone, organization, members, allowlist), allowed_senders, send_as, owners, active.",
  },
}

const acceptFromLabels: Record<AcceptFrom, string> = {
  anyone: "Anyone",
  organization: "This organization",
  members: "List members",
  allowlist: "Specific senders",
}

function summarizeList(values?: string[], limit = 2) {
  if (!values || values.length === 0) return "—"
  if (values.length <= limit) return values.join(", ")
  return `${values.slice(0, limit).join(", ")} +${values.length - limit}`
}

function signInLabel(op: BulkOperation) {
  if (op.kind === "shared") return "None (shared)"
  if (op.password) return "Local password"
  return "Sign in with provider"
}

function countLabel(count: number, singular: string, pluralWord: string) {
  return `${count} ${count === 1 ? singular : pluralWord}`
}

export function CsvImportDialog({
  orgId,
  kind,
  open,
  onOpenChange,
  defaultDomain,
  onCompleted,
}: {
  orgId: string
  kind: BulkOperationType
  open: boolean
  onOpenChange: (open: boolean) => void
  defaultDomain?: string
  onCompleted?: () => void
}) {
  const info = meta[kind]
  const fileRef = useRef<HTMLInputElement>(null)
  const [phase, setPhase] = useState<Phase>("upload")
  const [fileName, setFileName] = useState("")
  const [fileError, setFileError] = useState<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const [rows, setRows] = useState<RowStatus[]>([])
  const [filter, setFilter] = useState<Filter>("all")
  const [dryRunError, setDryRunError] = useState<string | null>(null)

  const validate = useMutation({
    mutationFn: async (parsed: ParsedCsvRow[]) => {
      const sendable: { idx: number; op: BulkOperation }[] = []
      parsed.forEach((r, idx) => {
        if (!r.parseError) sendable.push({ idx, op: r.operation })
      })
      if (sendable.length === 0) {
        return { sendable, response: null as BulkResponse | null }
      }
      const response = await api<BulkResponse>(`/organizations/${orgId}/bulk`, {
        method: "POST",
        body: { dryRun: true, operations: sendable.map((s) => s.op) },
      })
      return { sendable, response }
    },
    meta: { silent: true },
    onSuccess: ({ sendable, response }, parsed) => {
      setDryRunError(null)
      const byRowIndex = new Map<number, BulkResponse["results"][number]>()
      if (response) {
        response.results.forEach((res) => {
          const entry = sendable[res.index]
          if (entry) byRowIndex.set(entry.idx, res)
        })
      }
      setRows(
        parsed.map((row, idx) => {
          if (row.parseError) {
            return { row, state: "failed", address: row.operation.address ?? "", error: row.parseError }
          }
          const res = byRowIndex.get(idx)
          if (!res) {
            return { row, state: "failed", address: row.operation.address ?? "", error: "No validation result returned." }
          }
          if (res.status === "valid") {
            return { row, state: "valid", address: res.address ?? row.operation.address ?? "" }
          }
          return {
            row,
            state: "failed",
            address: res.address ?? row.operation.address ?? "",
            error: res.error?.message ?? "Validation failed.",
          }
        })
      )
    },
    onError: (err) => {
      setDryRunError(errorMessage(err))
    },
  })

  const execute = useMutation({
    mutationFn: async (current: RowStatus[]) => {
      const sendable: { idx: number; op: BulkOperation }[] = []
      current.forEach((item, idx) => {
        if (item.state === "valid") sendable.push({ idx, op: item.row.operation })
      })
      const response = await api<BulkResponse>(`/organizations/${orgId}/bulk`, {
        method: "POST",
        body: { dryRun: false, operations: sendable.map((s) => s.op) },
      })
      return { sendable, response }
    },
    onSuccess: ({ sendable, response }, current) => {
      const byRowIndex = new Map<number, BulkResponse["results"][number]>()
      response.results.forEach((res) => {
        const entry = sendable[res.index]
        if (entry) byRowIndex.set(entry.idx, res)
      })
      const finalRows: RowStatus[] = current.map((item, idx) => {
        const res = byRowIndex.get(idx)
        if (!res) return item
        if (res.status === "created") {
          return { ...item, state: "created", address: res.address ?? item.address, error: undefined }
        }
        return {
          ...item,
          state: "failed",
          address: res.address ?? item.address,
          error: res.error?.message ?? "Import failed.",
        }
      })
      setRows(finalRows)
      setFilter("all")
      setPhase("report")
      const createdCount = finalRows.filter((r) => r.state === "created").length
      const failedCount = finalRows.filter((r) => r.state === "failed").length
      if (createdCount > 0) onCompleted?.()
      if (failedCount === 0) {
        toast.success(`Imported ${countLabel(createdCount, info.noun, info.pluralNoun)}`)
      } else if (createdCount > 0) {
        toast.warning(`Imported ${countLabel(createdCount, info.noun, info.pluralNoun)}, ${failedCount} failed`)
      } else {
        toast.error(`No ${info.pluralNoun} were imported (${failedCount} failed)`)
      }
    },
  })

  function reset() {
    setPhase("upload")
    setFileName("")
    setFileError(null)
    setDragging(false)
    setRows([])
    setFilter("all")
    setDryRunError(null)
    validate.reset()
    execute.reset()
  }

  function handleOpenChange(next: boolean) {
    if (!next && execute.isPending) return
    onOpenChange(next)
    if (!next) reset()
  }

  async function handleFile(file: File) {
    setFileError(null)
    setDryRunError(null)
    if (file.size > MAX_CSV_BYTES) {
      setRows([])
      setPhase("upload")
      setFileError("Choose a CSV file smaller than 4 MB.")
      return
    }
    const text = await file.text()
    const { rows: parsed, fileError: err } = parseImportCsv(kind, text)
    if (err) {
      setRows([])
      setPhase("upload")
      setFileError(err)
      return
    }
    setFileName(file.name)
    setFilter("all")
    setRows(
      parsed.map((row) => ({
        row,
        state: row.parseError ? "failed" : "pending",
        address: row.operation.address ?? "",
        error: row.parseError,
      }))
    )
    setPhase("preview")
    validate.mutate(parsed)
  }

  const okCount = rows.filter((r) => r.state === "valid" || r.state === "created").length
  const failedCount = rows.filter((r) => r.state === "failed").length
  const visibleRows = rows.filter((r) => {
    if (filter === "ok") return r.state === "valid" || r.state === "created"
    if (filter === "failed") return r.state === "failed"
    return true
  })

  const filterOptions = [
    { value: "all" as const, label: `All (${rows.length})` },
    { value: "ok" as const, label: `${phase === "report" ? "Created" : "Ready"} (${okCount})` },
    { value: "failed" as const, label: `${phase === "report" ? "Failed" : "Errors"} (${failedCount})` },
  ]

  function downloadFailed() {
    const failed = rows
      .filter((r) => r.state === "failed")
      .map((r) => ({ raw: r.row.raw, error: r.error ?? "Failed" }))
    downloadCsv(info.failedFile, failedRowsCsv(kind, failed))
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-[900px]">
        <DialogHeader>
          <DialogTitle>{info.title}</DialogTitle>
          <DialogDescription>{info.hint}</DialogDescription>
        </DialogHeader>

        <input
          ref={fileRef}
          type="file"
          accept=".csv,text/csv"
          hidden
          onChange={(e) => {
            const file = e.target.files?.[0]
            if (file) void handleFile(file)
            e.target.value = ""
          }}
        />

        {phase === "upload" && (
          <div className="flex flex-col gap-4">
            <div
              role="button"
              tabIndex={0}
              onClick={() => fileRef.current?.click()}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault()
                  fileRef.current?.click()
                }
              }}
              onDragOver={(e) => {
                e.preventDefault()
                setDragging(true)
              }}
              onDragLeave={() => setDragging(false)}
              onDrop={(e) => {
                e.preventDefault()
                setDragging(false)
                const file = e.dataTransfer.files?.[0]
                if (file) void handleFile(file)
              }}
              className={`flex cursor-pointer flex-col items-center justify-center gap-3 rounded-xl border border-dashed px-6 py-10 text-center transition-colors ${
                dragging ? "border-ember bg-ember/6" : "border-strong bg-muted/30 hover:border-fg-tertiary hover:bg-muted/50"
              }`}
            >
              <Upload aria-hidden className="size-8 text-fg-tertiary" />
              <div className="flex flex-col gap-1">
                <span className="font-semibold">Choose a CSV file or drop it here</span>
                <span className="text-sm text-fg-tertiary">Up to 1,000 rows per import. Every row is validated before anything is created.</span>
              </div>
              <Button type="button" variant="secondary" size="sm" onClick={(e) => { e.stopPropagation(); fileRef.current?.click() }}>
                Select CSV file
              </Button>
            </div>

            {fileError && <p role="alert" className="text-sm font-medium text-danger">{fileError}</p>}

            <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-card px-4 py-3">
              <div className="flex min-w-0 flex-col gap-1">
                <span className="text-sm font-medium">Expected columns</span>
                <code className="truncate font-mono text-xs text-fg-tertiary">{csvHeaders[kind].join(", ")}</code>
              </div>
              <Button type="button" variant="secondary" size="sm" onClick={() => downloadCsv(info.templateFile, templateCsv(kind, defaultDomain))}>
                <Download aria-hidden />
                Download template
              </Button>
            </div>
          </div>
        )}

        {(phase === "preview" || phase === "report") && (
          <div className="flex min-w-0 flex-col gap-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="inline-flex items-center gap-2 text-sm font-medium">
                  <FileSpreadsheet aria-hidden className="size-4 text-fg-tertiary" />
                  {fileName}
                </span>
                <StatusBadge tone="neutral">{rows.length} total</StatusBadge>
                {!validate.isPending && !dryRunError && (
                  <>
                    <StatusBadge tone={okCount > 0 ? "success" : "neutral"}>
                      {okCount} {phase === "report" ? "created" : "ready"}
                    </StatusBadge>
                    {failedCount > 0 && (
                      <StatusBadge tone="danger">
                        {failedCount} {phase === "report" ? "failed" : failedCount === 1 ? "error" : "errors"}
                      </StatusBadge>
                    )}
                  </>
                )}
              </div>
              {phase === "preview" && (
                <Button type="button" variant="secondary" size="sm" disabled={execute.isPending} onClick={() => fileRef.current?.click()}>
                  Choose another file
                </Button>
              )}
            </div>

            {!validate.isPending && !dryRunError && okCount > 0 && failedCount > 0 && (
              <SegmentedControl label="Filter rows" options={filterOptions} value={filter} onChange={setFilter} />
            )}

            {validate.isPending ? (
              <RowsSkeleton rows={4} />
            ) : dryRunError ? (
              <div className="flex flex-col items-start gap-3 rounded-xl border border-danger/30 bg-danger/8 p-4">
                <p className="text-sm text-danger">{dryRunError}</p>
                <Button type="button" variant="secondary" size="sm" onClick={() => validate.mutate(rows.map((r) => r.row))}>
                  <RotateCcw aria-hidden />
                  Retry validation
                </Button>
              </div>
            ) : (
              <div className="max-h-[400px] overflow-auto rounded-xl border border-border bg-card">
                <table data-slot="table" className="w-full caption-bottom text-base">
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-14">Row</TableHead>
                      <TableHead>{kind === "list" ? "List" : "Address"}</TableHead>
                      {kind === "mailbox" && (
                        <>
                          <TableHead>Type</TableHead>
                          <TableHead>Display name</TableHead>
                          <TableHead>Quota</TableHead>
                          <TableHead>Sign-in</TableHead>
                        </>
                      )}
                      {kind === "alias" && <TableHead>Destinations</TableHead>}
                      {kind === "list" && (
                        <>
                          <TableHead>Members</TableHead>
                          <TableHead>Who can send</TableHead>
                          <TableHead>Owners</TableHead>
                        </>
                      )}
                      <TableHead>Status</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {visibleRows.map((item) => {
                      const op = item.row.operation
                      return (
                        <TableRow key={item.row.rowNumber}>
                          <TableCell className="font-mono text-xs text-fg-tertiary">{item.row.rowNumber}</TableCell>
                          <TableCell>
                            {kind === "list" ? (
                              <div className="flex flex-col">
                                <span className="font-medium">{op.name || item.address || "—"}</span>
                                {op.name && <span className="font-mono text-xs text-fg-tertiary">{item.address || "—"}</span>}
                              </div>
                            ) : (
                              <div className="flex items-center gap-2">
                                <span className="font-mono text-sm">{item.address || "—"}</span>
                                {kind === "alias" && item.address.startsWith("@") && <StatusBadge tone="info">Catch-all</StatusBadge>}
                              </div>
                            )}
                          </TableCell>
                          {kind === "mailbox" && (
                            <>
                              <TableCell>{op.kind === "shared" ? <StatusBadge tone="neutral">Shared</StatusBadge> : "User"}</TableCell>
                              <TableCell>{op.displayName || "—"}</TableCell>
                              <TableCell>{op.quotaBytes ? formatBytes(op.quotaBytes) : "No limit"}</TableCell>
                              <TableCell className="text-sm text-fg-secondary">{signInLabel(op)}</TableCell>
                            </>
                          )}
                          {kind === "alias" && (
                            <TableCell className="max-w-[260px] truncate font-mono text-xs">{summarizeList(op.destinations, 3)}</TableCell>
                          )}
                          {kind === "list" && (
                            <>
                              <TableCell className="text-sm">
                                {op.destinations?.length ? `${plural(op.destinations.length, "member")}` : "0 members"}
                              </TableCell>
                              <TableCell className="text-sm">{acceptFromLabels[op.acceptFrom ?? "anyone"]}</TableCell>
                              <TableCell className="max-w-[180px] truncate font-mono text-xs">{summarizeList(op.owners, 2)}</TableCell>
                            </>
                          )}
                          <TableCell className="whitespace-normal">
                            <div className="flex flex-col gap-1">
                              {item.state === "valid" && <StatusBadge tone="success">Ready</StatusBadge>}
                              {item.state === "created" && <StatusBadge tone="success">Created</StatusBadge>}
                              {item.state === "failed" && (
                                <>
                                  <StatusBadge tone="danger">{phase === "report" ? "Failed" : "Error"}</StatusBadge>
                                  {item.error && <span className="text-xs text-danger">{item.error}</span>}
                                </>
                              )}
                            </div>
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </table>
              </div>
            )}

            {phase === "preview" && !validate.isPending && !dryRunError && failedCount > 0 && okCount > 0 && (
              <p className="text-sm text-fg-secondary">
                {plural(failedCount, "row")} with errors will be skipped and included in the final report so you can fix and re-import them.
              </p>
            )}
          </div>
        )}

        <DialogFooter>
          {phase === "upload" && (
            <DialogClose asChild>
              <Button type="button" variant="secondary">
                Cancel
              </Button>
            </DialogClose>
          )}
          {phase === "preview" && (
            <>
              <DialogClose asChild>
                <Button type="button" variant="secondary" disabled={execute.isPending}>
                  Cancel
                </Button>
              </DialogClose>
              <Button
                type="button"
                loading={execute.isPending}
                disabled={validate.isPending || !!dryRunError || okCount === 0}
                onClick={() => execute.mutate(rows)}
              >
                Import {countLabel(okCount, info.noun, info.pluralNoun)}
              </Button>
            </>
          )}
          {phase === "report" && (
            <>
              {failedCount > 0 && (
                <Button type="button" variant="secondary" onClick={downloadFailed}>
                  <Download aria-hidden />
                  Download failed rows
                </Button>
              )}
              <DialogClose asChild>
                <Button type="button">Done</Button>
              </DialogClose>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

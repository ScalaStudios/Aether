"use client"

import { useMutation, useQueryClient, type UseQueryResult } from "@tanstack/react-query"
import { CalendarClock, Ellipsis, FolderInput, FolderPen, FolderPlus, FolderX, MailOpen, PenSquare, Trash2, type LucideIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { FolderDialog, folderLeaf, type FolderAction } from "@/components/mail/folder-dialog"
import { ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useSession } from "@/hooks/use-session"
import { api, errorMessage } from "@/lib/api"
import { dragTypes, folderIcons, folderLabel, folderParent, folderTree, snoozedFolder } from "@/lib/mail"
import type { Folder, Mailbox } from "@/lib/types"

type FolderRequest = { method: "POST" | "PATCH" | "DELETE"; path?: string; folder?: string; moveTo?: string; body?: object; done: string }

function FolderButton({
  icon: Icon,
  label,
  count,
  countLabel,
  active,
  depth = 0,
  className,
  ...props
}: { icon: LucideIcon; label: string; count: number; countLabel: string; active: boolean; depth?: number } & React.ComponentProps<"button">) {
  return (
    <button
      type="button"
      aria-current={active ? "page" : undefined}
      style={depth ? { paddingLeft: 12 + depth * 16 } : undefined}
      className={cn(
        "flex min-h-10 w-full cursor-pointer items-center gap-3 rounded-lg px-3 text-left text-[15px] font-medium text-fg-secondary transition-colors duration-160 hover:bg-muted hover:text-foreground max-md:min-h-11",
        active && "bg-muted text-foreground",
        className
      )}
      {...props}
    >
      <Icon aria-hidden className={cn("size-4 shrink-0 text-fg-tertiary", active && "text-ember")} />
      <span className="flex-1 truncate">{label}</span>
      {count > 0 && (
        <span className="text-xs font-semibold tabular-nums text-foreground md:group-hover/folder:invisible md:group-has-[:focus-visible]/folder:invisible md:group-has-[[aria-expanded=true]]/folder:invisible">
          {count}
          <span className="sr-only"> {countLabel}</span>
        </span>
      )}
    </button>
  )
}

function FolderMenu({ folder, label, open, onOpenChange, onAction, onMarkRead }: {
  folder: Folder
  label: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onAction: (kind: FolderAction["kind"]) => void
  onMarkRead: () => void
}) {
  const fixed = folder.specialUse !== "" || folder.name === snoozedFolder
  const permanent = folder.specialUse === "trash" || folder.specialUse === "junk"
  return (
    <DropdownMenu open={open} onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="quiet"
          size="icon-sm"
          aria-label={`More actions for ${label}`}
          className="absolute top-1/2 right-1 -translate-y-1/2 md:bg-muted md:opacity-0 md:group-hover/folder:opacity-100 md:group-has-[:focus-visible]/folder:opacity-100 md:aria-expanded:opacity-100"
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuItem onSelect={() => onAction("create")}>
          <FolderPlus aria-hidden />
          New subfolder…
        </DropdownMenuItem>
        {!fixed && (
          <>
            <DropdownMenuItem onSelect={() => onAction("rename")}>
              <FolderPen aria-hidden />
              Rename…
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onAction("move")}>
              <FolderInput aria-hidden />
              Move…
            </DropdownMenuItem>
          </>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem disabled={folder.unread === 0} onSelect={onMarkRead}>
          <MailOpen aria-hidden />
          Mark all as read
        </DropdownMenuItem>
        <DropdownMenuItem disabled={folder.total === 0} onSelect={() => onAction("empty")}>
          <Trash2 aria-hidden />
          {permanent ? `Empty ${folderLabel(folder)}…` : "Empty folder…"}
        </DropdownMenuItem>
        {!fixed && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={() => onAction("delete")}>
              <FolderX aria-hidden />
              Delete…
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function FolderPane({
  className,
  mailboxes,
  mailbox,
  onMailboxChange,
  folders,
  folder,
  onFolderChange,
  scheduled = 0,
  scheduledActive = false,
  onScheduled,
  onCompose,
  onDropMessage,
}: {
  className?: string
  mailboxes: Mailbox[]
  mailbox: Mailbox
  onMailboxChange: (id: string) => void
  folders: UseQueryResult<Folder[]>
  folder: string
  onFolderChange: (name: string) => void
  scheduled?: number
  scheduledActive?: boolean
  onScheduled?: () => void
  onCompose?: () => void
  onDropMessage?: (uid: number, folder: Folder) => void
}) {
  const userId = useSession().data?.user.id
  const qc = useQueryClient()
  const editable = mailbox.access !== "read"
  const [action, setAction] = useState<FolderAction | null>(null)
  const [error, setError] = useState("")
  const [menuFor, setMenuFor] = useState<string | null>(null)
  const [dragging, setDragging] = useState<string | null>(null)
  const [dropOn, setDropOn] = useState<string | null>(null)
  const labelOf = (name: string) => folderLabel(folders.data?.find((f) => f.name === name) ?? { name, specialUse: "" })

  const manage = useMutation({
    mutationFn: (r: FolderRequest) =>
      api<{ name: string } | undefined>(`/mailboxes/${mailbox.id}/folders${r.path ?? ""}`, { method: r.method, query: { folder: r.folder, moveTo: r.moveTo }, body: r.body }),
    meta: { silent: true },
    onSuccess: (data, r) => {
      toast.success(r.done)
      setAction(null)
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      if (!r.folder || (folder !== r.folder && !folder.startsWith(`${r.folder}/`))) return
      if (r.method === "PATCH" && data) onFolderChange(data.name + folder.slice(r.folder.length))
      if (r.method === "DELETE") onFolderChange(folders.data?.find((f) => f.specialUse === "inbox")?.name ?? "INBOX")
    },
    onError: (e) => (action ? setError(errorMessage(e)) : toast.error(errorMessage(e))),
  })

  function move(f: Folder, parent: string) {
    manage.mutate({ method: "PATCH", folder: f.name, body: { parent }, done: parent ? `Moved ${folderLeaf(f)} into ${labelOf(parent)}` : `Moved ${folderLeaf(f)} to the top level` })
  }

  function submit(a: FolderAction, v: { name?: string; parent?: string; moveTo?: string }) {
    setError("")
    const f = a.folder
    const name = f?.name ?? ""
    if (a.kind === "create") manage.mutate({ method: "POST", body: { name: v.name, parent: f?.name }, done: `Created ${v.name?.trim()}` })
    if (a.kind === "rename") manage.mutate({ method: "PATCH", folder: name, body: { name: v.name }, done: `Renamed to ${v.name?.trim()}` })
    if (a.kind === "move" && f) move(f, v.parent ?? "")
    if (a.kind === "delete" && f) manage.mutate({ method: "DELETE", folder: name, moveTo: v.moveTo, done: `Deleted ${folderLeaf(f)}` })
    if (a.kind === "empty" && f) manage.mutate({ method: "POST", path: "/empty", folder: name, done: `Emptied ${folderLabel(f)}` })
  }

  function start(next: FolderAction) {
    setError("")
    setAction(next)
  }

  function canDrop(e: React.DragEvent, target: Folder) {
    if (e.dataTransfer.types.includes(dragTypes.message)) return !!onDropMessage && target.name !== folder
    return !!dragging && e.dataTransfer.types.includes(dragTypes.folder) && !`${target.name}/`.startsWith(`${dragging}/`) && folderParent(dragging) !== target.name
  }

  function drop(e: React.DragEvent, target: Folder) {
    e.preventDefault()
    setDropOn(null)
    const uid = Number(e.dataTransfer.getData(dragTypes.message))
    const source = folders.data?.find((f) => f.name === e.dataTransfer.getData(dragTypes.folder))
    if (uid) onDropMessage?.(uid, target)
    else if (source) move(source, target.name)
  }

  return (
    <aside aria-label="Folders" className={cn("min-h-0 flex-col gap-6 border-r border-border bg-card p-4", className)}>
      {mailboxes.length > 1 ? (
        <Select value={mailbox.id} onValueChange={onMailboxChange}>
          <SelectTrigger aria-label="Mailbox" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {mailboxes.map((m) => (
              <SelectItem key={m.id} value={m.id}>
                <span className="truncate">{m.address}</span>
                {userId && m.ownerUserId !== userId && <span className="text-xs text-fg-tertiary">Shared</span>}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <p className="truncate px-2 text-sm text-fg-tertiary">{mailbox.address}</p>
      )}
      {onCompose ? (
        <Button onClick={onCompose} className="w-full">
          <PenSquare aria-hidden />
          New message
        </Button>
      ) : (
        <p className="rounded-lg bg-muted px-3 py-2 text-sm text-fg-secondary">Read only. You can read this mailbox but not send, move, or delete.</p>
      )}
      <nav aria-label="Mail folders" className="-mx-2 flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2">
        {folders.isPending ? (
          <RowsSkeleton rows={5} />
        ) : folders.isError ? (
          <ErrorState error={folders.error} onRetry={() => folders.refetch()} className="py-6" />
        ) : (
          <>
            {folderTree(folders.data).map(({ folder: f, depth, label }) => (
              <div
                key={f.name}
                className={cn("relative rounded-lg", editable && "group/folder", dropOn === f.name && "ring-1 ring-ember")}
                onContextMenu={(e) => {
                  if (!editable) return
                  e.preventDefault()
                  setMenuFor(f.name)
                }}
                onDragOver={(e) => {
                  if (!canDrop(e, f)) return
                  e.preventDefault()
                  setDropOn(f.name)
                }}
                onDragLeave={(e) => !e.currentTarget.contains(e.relatedTarget as Node | null) && setDropOn(null)}
                onDrop={(e) => drop(e, f)}
              >
                <FolderButton
                  icon={folderIcons[f.specialUse]}
                  label={label}
                  count={f.unread}
                  countLabel="unread"
                  active={!scheduledActive && f.name === folder}
                  depth={depth}
                  className={cn(editable && "max-md:pr-14")}
                  draggable={editable && f.specialUse === "" && f.name !== snoozedFolder}
                  onDragStart={(e) => {
                    e.dataTransfer.setData(dragTypes.folder, f.name)
                    setDragging(f.name)
                  }}
                  onDragEnd={() => setDragging(null)}
                  onClick={() => onFolderChange(f.name)}
                />
                {editable && (
                  <FolderMenu
                    folder={f}
                    label={label}
                    open={menuFor === f.name}
                    onOpenChange={(next) => setMenuFor(next ? f.name : null)}
                    onAction={(kind) => start({ kind, folder: f })}
                    onMarkRead={() => manage.mutate({ method: "POST", path: "/mark-read", folder: f.name, done: `Marked everything in ${label} as read` })}
                  />
                )}
              </div>
            ))}
            {scheduled > 0 && onScheduled && (
              <FolderButton icon={CalendarClock} label="Scheduled" count={scheduled} countLabel="scheduled" active={scheduledActive} onClick={onScheduled} />
            )}
            {editable && (
              <FolderButton icon={FolderPlus} label="New folder" count={0} countLabel="" active={false} className="text-fg-tertiary" onClick={() => start({ kind: "create" })} />
            )}
          </>
        )}
      </nav>
      {action && (
        <FolderDialog
          key={`${action.kind}:${action.folder?.name}`}
          action={action}
          folders={folders.data ?? []}
          pending={manage.isPending}
          error={error}
          onClose={() => setAction(null)}
          onSubmit={(values) => submit(action, values)}
        />
      )}
    </aside>
  )
}

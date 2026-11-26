"use client"

import { useMutation, useQueryClient, type UseQueryResult } from "@tanstack/react-query"
import { ArchiveRestore, CalendarClock, Ellipsis, FolderInput, FolderPen, FolderPlus, FolderX, MailOpen, Newspaper, PenSquare, ShieldAlert, Trash2, type LucideIcon } from "lucide-react"
import Link from "next/link"
import { useState } from "react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"

import { FolderDialog, folderLeaf, type FolderAction } from "@/components/mail/folder-dialog"
import { RecoverDialog } from "@/components/mail/recover-dialog"
import { MailOrganize } from "@/components/mail/organize"
import { NewslettersDialog } from "@/components/mail/newsletters-dialog"
import { ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { tierText, UsageBar, usagePercent } from "@/components/usage-bar"
import { useSession } from "@/hooks/use-session"
import { api, errorMessage } from "@/lib/api"
import { useI18n } from "@/lib/i18n"
import { formatBytes } from "@/lib/format"
import { dragTypes, folderIcons, folderLabel, folderParent, folderTree, messageDragFolder, snoozedFolder, storageTier } from "@/lib/mail"
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

function FolderMenu({ folder, label, open, onOpenChange, onAction, onMarkRead, onRecover }: {
  folder: Folder
  label: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onAction: (kind: FolderAction["kind"]) => void
  onMarkRead: () => void
  onRecover: () => void
}) {
  const { t } = useI18n()
  const fixed = folder.specialUse !== "" || folder.name === snoozedFolder
  const permanent = folder.specialUse === "trash" || folder.specialUse === "junk"
  return (
    <DropdownMenu open={open} onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="quiet"
          size="icon-sm"
          aria-label={t("mail.folders.moreActionsFor", { label })}
          className="absolute top-1/2 right-1 -translate-y-1/2 md:bg-muted md:opacity-0 md:group-hover/folder:opacity-100 md:group-has-[:focus-visible]/folder:opacity-100 md:aria-expanded:opacity-100"
        >
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuItem onSelect={() => onAction("create")}>
          <FolderPlus aria-hidden />
          {t("mail.folders.newSubfolder")}
        </DropdownMenuItem>
        {!fixed && (
          <>
            <DropdownMenuItem onSelect={() => onAction("rename")}>
              <FolderPen aria-hidden />
              {t("mail.folders.renameEllipsis")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onAction("move")}>
              <FolderInput aria-hidden />
              {t("mail.folders.moveEllipsis")}
            </DropdownMenuItem>
          </>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem disabled={folder.unread === 0} onSelect={onMarkRead}>
          <MailOpen aria-hidden />
          {t("mail.folders.markAllRead")}
        </DropdownMenuItem>
        <DropdownMenuItem disabled={folder.total === 0} onSelect={() => onAction("empty")}>
          <Trash2 aria-hidden />
          {permanent ? t("mail.folders.emptyNamed", { name: folderLabel(folder) }) : t("mail.folders.emptyFolder")}
        </DropdownMenuItem>
        {folder.specialUse === "trash" && (
          <DropdownMenuItem onSelect={onRecover}>
            <ArchiveRestore aria-hidden />
            Recover deleted items…
          </DropdownMenuItem>
        )}
        {!fixed && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={() => onAction("delete")}>
              <FolderX aria-hidden />
              {t("mail.folders.deleteEllipsis")}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

type Tree = { mailbox: Mailbox; folders: UseQueryResult<Folder[]> }

function StorageSummary({ mailbox, folders }: { mailbox: Mailbox; folders: Folder[] }) {
  const used = folders.reduce((n, f) => n + f.size, 0)
  const quota = mailbox.quotaBytes
  const tier = storageTier(used, quota, mailbox.quotaWarningPercents)
  return (
    <Link href="/settings#storage" className="flex flex-col gap-2 rounded-lg px-3 py-2 transition-colors duration-160 hover:bg-muted">
      <span className="flex items-baseline justify-between gap-3 text-xs">
        <span className="font-semibold text-fg-secondary">Storage</span>
        {quota > 0 && <span className={cn("font-semibold tabular-nums", tierText[tier])}>{usagePercent(used, quota)}%</span>}
      </span>
      {quota > 0 && <UsageBar used={used} quota={quota} tier={tier} />}
      <span className="text-xs text-fg-tertiary tabular-nums">{quota ? `${formatBytes(used)} of ${formatBytes(quota)} used` : `${formatBytes(used)} used · No limit`}</span>
    </Link>
  )
}

function MailboxFolders({
  mailbox,
  folders,
  folder,
  onFolderChange,
  onDropMessage,
  scheduled = 0,
  scheduledActive = false,
  onScheduled,
  quarantine = 0,
  quarantineActive = false,
  onQuarantine,
  folderActive = true,
  browse = null,
  onBrowseLabel,
  onBrowseSearch,
}: {
  mailbox: Mailbox
  folders: UseQueryResult<Folder[]>
  folder: string | null
  onFolderChange: (name: string) => void
  onDropMessage?: (uid: number, folder: Folder, sourceFolder: string) => void
  scheduled?: number
  scheduledActive?: boolean
  onScheduled?: () => void
  quarantine?: number
  quarantineActive?: boolean
  onQuarantine?: () => void
  folderActive?: boolean
  browse?: { kind: "label" | "search"; id: string } | null
  onBrowseLabel?: (id: string) => void
  onBrowseSearch?: (id: string) => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const editable = mailbox.access !== "read"
  const [action, setAction] = useState<FolderAction | null>(null)
  const [error, setError] = useState("")
  const [menuFor, setMenuFor] = useState<string | null>(null)
  const [recovering, setRecovering] = useState(false)
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
      qc.invalidateQueries({ queryKey: ["saved-searches", mailbox.id] })
      if (folder === null || !r.folder || (folder !== r.folder && !folder.startsWith(`${r.folder}/`))) return
      if (r.method === "PATCH" && data) onFolderChange(data.name + folder.slice(r.folder.length))
      if (r.method === "DELETE") onFolderChange(folders.data?.find((f) => f.specialUse === "inbox")?.name ?? "INBOX")
    },
    onError: (e) => (action ? setError(errorMessage(e)) : toast.error(errorMessage(e))),
  })

  function move(f: Folder, parent: string) {
    manage.mutate({
      method: "PATCH",
      folder: f.name,
      body: { parent },
      done: parent
        ? t("mail.folders.toasts.movedInto", { name: folderLeaf(f), parent: labelOf(parent) })
        : t("mail.folders.toasts.movedTopLevel", { name: folderLeaf(f) }),
    })
  }

  function submit(a: FolderAction, v: { name?: string; parent?: string; moveTo?: string }) {
    setError("")
    const f = a.folder
    const name = f?.name ?? ""
    if (a.kind === "create") manage.mutate({ method: "POST", body: { name: v.name, parent: f?.name }, done: t("mail.folders.toasts.created", { name: v.name?.trim() ?? "" }) })
    if (a.kind === "rename") manage.mutate({ method: "PATCH", folder: name, body: { name: v.name }, done: t("mail.folders.toasts.renamed", { name: v.name?.trim() ?? "" }) })
    if (a.kind === "move" && f) move(f, v.parent ?? "")
    if (a.kind === "delete" && f) manage.mutate({ method: "DELETE", folder: name, moveTo: v.moveTo, done: t("mail.folders.toasts.deleted", { name: folderLeaf(f) }) })
    if (a.kind === "empty" && f) manage.mutate({ method: "POST", path: "/empty", folder: name, done: t("mail.folders.toasts.emptied", { name: folderLabel(f) }) })
  }

  function start(next: FolderAction) {
    setError("")
    setAction(next)
  }

  function canDrop(e: React.DragEvent, target: Folder) {
    if (e.dataTransfer.types.includes(dragTypes.message)) return !!onDropMessage && editable && messageDragFolder() !== target.name
    return !!dragging && e.dataTransfer.types.includes(dragTypes.folder) && !`${target.name}/`.startsWith(`${dragging}/`) && folderParent(dragging) !== target.name
  }

  function drop(e: React.DragEvent, target: Folder) {
    e.preventDefault()
    setDropOn(null)
    const uid = Number(e.dataTransfer.getData(dragTypes.message))
    const source = folders.data?.find((f) => f.name === e.dataTransfer.getData(dragTypes.folder))
    if (uid) onDropMessage?.(uid, target, e.dataTransfer.getData(dragTypes.messageFolder))
    else if (source) move(source, target.name)
  }

  if (folders.isPending) return <RowsSkeleton rows={3} />
  if (folders.isError) return <ErrorState error={folders.error} onRetry={() => folders.refetch()} className="py-6" />

  return (
    <div className="flex flex-col gap-1">
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
            countLabel={t("mail.folders.unread")}
            active={folderActive && !scheduledActive && !quarantineActive && f.name === folder}
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
              onMarkRead={() => manage.mutate({ method: "POST", path: "/mark-read", folder: f.name, done: t("mail.folders.toasts.markedAllRead", { name: label }) })}
              onRecover={() => setRecovering(true)}
            />
          )}
        </div>
      ))}
      {scheduled > 0 && onScheduled && (
        <FolderButton icon={CalendarClock} label={t("mail.folders.scheduled")} count={scheduled} countLabel={t("mail.folders.scheduledCount")} active={scheduledActive} onClick={onScheduled} />
      )}
      {onQuarantine && <FolderButton icon={ShieldAlert} label="Quarantine" count={quarantine} countLabel="held" active={quarantineActive} onClick={onQuarantine} />}
      {editable && (
        <FolderButton icon={FolderPlus} label={t("mail.folders.newFolder")} count={0} countLabel="" active={false} className="text-fg-tertiary" onClick={() => start({ kind: "create" })} />
      )}
      <MailOrganize mailbox={mailbox} active={browse} onLabel={(id) => onBrowseLabel?.(id)} onSearch={(id) => onBrowseSearch?.(id)} />
      {action && (
        <FolderDialog
          key={`${action.kind}:${action.folder?.name}`}
          action={action}
          folders={folders.data}
          pending={manage.isPending}
          error={error}
          onClose={() => setAction(null)}
          onSubmit={(values) => submit(action, values)}
        />
      )}
      <RecoverDialog mailbox={recovering ? mailbox : null} onOpenChange={setRecovering} />
    </div>
  )
}

export function FolderPane({
  className,
  own,
  primary,
  onPrimaryChange,
  shared,
  active,
  onFolderChange,
  scheduled = 0,
  scheduledActive = false,
  onScheduled,
  quarantine = 0,
  quarantineActive = false,
  onQuarantine,
  onCompose,
  onDropMessage,
  browse = null,
  onBrowseLabel,
  onBrowseSearch,
}: {
  className?: string
  own: Mailbox[]
  primary?: Tree
  onPrimaryChange: (id: string) => void
  shared: Tree[]
  active: { mailboxId: string; folder: string }
  onFolderChange: (mailboxId: string, folder: string) => void
  scheduled?: number
  scheduledActive?: boolean
  onScheduled?: () => void
  quarantine?: number
  quarantineActive?: boolean
  onQuarantine?: () => void
  onCompose?: () => void
  onDropMessage?: (uid: number, folder: Folder, mailboxId: string, sourceFolder: string) => void
  browse?: { kind: "label" | "search"; id: string } | null
  onBrowseLabel?: (mailboxId: string, id: string) => void
  onBrowseSearch?: (mailboxId: string, id: string) => void
}) {
  const { t } = useI18n()
  const userId = useSession().data?.user.id
  const readOnly = (shared.find((t) => t.mailbox.id === active.mailboxId) ?? primary)?.mailbox.access === "read"
  const [newslettersOpen, setNewslettersOpen] = useState(false)

  const tree = ({ mailbox, folders }: Tree) => {
    const current = mailbox.id === active.mailboxId
    return (
      <MailboxFolders
        mailbox={mailbox}
        folders={folders}
        folder={current ? active.folder : null}
        onFolderChange={(name) => onFolderChange(mailbox.id, name)}
        onDropMessage={onDropMessage && ((uid, f, sourceFolder) => onDropMessage(uid, f, mailbox.id, sourceFolder))}
        scheduled={current ? scheduled : 0}
        scheduledActive={current && scheduledActive}
        onScheduled={onScheduled}
        quarantine={current ? quarantine : 0}
        quarantineActive={current && quarantineActive}
        onQuarantine={current ? onQuarantine : undefined}
        folderActive={current && !browse}
        browse={current ? browse : null}
        onBrowseLabel={(id) => onBrowseLabel?.(mailbox.id, id)}
        onBrowseSearch={(id) => onBrowseSearch?.(mailbox.id, id)}
      />
    )
  }

  return (
    <aside aria-label={t("mail.folders.sidebarLabel")} className={cn("min-h-0 flex-col gap-6 border-r border-border bg-card p-4", className)}>
      {primary &&
        (own.length > 1 ? (
          <Select value={primary.mailbox.id} onValueChange={onPrimaryChange}>
            <SelectTrigger aria-label={t("mail.folders.mailbox")} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {own.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  <span className="truncate">{m.address}</span>
                  {userId && m.ownerUserId !== userId && <span className="text-xs text-fg-tertiary">{t("mail.folders.delegated")}</span>}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <p className="truncate px-2 text-sm text-fg-tertiary">{primary.mailbox.address}</p>
        ))}
      {onCompose ? (
        <Button onClick={onCompose} className="w-full">
          <PenSquare aria-hidden />
          {t("mail.folders.newMessage")}
        </Button>
      ) : (
        readOnly && <p className="rounded-lg bg-muted px-3 py-2 text-sm text-fg-secondary">{t("mail.folders.readOnlyNotice")}</p>
      )}
      {active.mailboxId && (
        <Button variant="quiet" className="w-full" onClick={() => setNewslettersOpen(true)}>
          <Newspaper aria-hidden />
          Newsletters
        </Button>
      )}
      <NewslettersDialog open={newslettersOpen} onOpenChange={setNewslettersOpen} mailboxId={active.mailboxId} readOnly={readOnly} />
      <nav aria-label={t("mail.folders.navLabel")} className="-mx-2 flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-2">
        {primary && tree(primary)}
        {shared.map((item) => {
          const unread = item.folders.data?.reduce((n, f) => n + f.unread, 0) ?? 0
          return (
            <section key={item.mailbox.id} aria-labelledby={`mailbox-${item.mailbox.id}`} className="flex flex-col gap-2">
              <div className="flex items-start gap-3 px-3">
                <h2 id={`mailbox-${item.mailbox.id}`} className="flex min-w-0 flex-1 flex-col text-label text-fg-tertiary">
                  <span className="truncate">{item.mailbox.displayName || item.mailbox.address}</span>
                  {item.mailbox.displayName && <span className="truncate font-normal">{item.mailbox.address}</span>}
                </h2>
                {unread > 0 && (
                  <span className="text-xs font-semibold tabular-nums text-foreground">
                    {unread}
                    <span className="sr-only"> {t("mail.folders.unread")}</span>
                  </span>
                )}
              </div>
              {tree(item)}
            </section>
          )
        })}
      </nav>
      {primary?.folders.data && <StorageSummary mailbox={primary.mailbox} folders={primary.folders.data} />}
    </aside>
  )
}

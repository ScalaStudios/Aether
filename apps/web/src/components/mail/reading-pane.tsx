"use client"

import { useMutation, useQueries, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query"
import { AlarmClock, AlarmClockOff, ArrowLeft, AtSign, Ban, ChevronDown, Copy, Download, Flag, FolderInput, Forward, Globe, Link2, Mail, MailOpen, OctagonAlert, Reply, ReplyAll, ShieldCheck, Trash2 } from "lucide-react"
import { cn } from "@/lib/utils"
import { useState } from "react"
import { toast } from "sonner"

import { Avatar } from "@/components/avatar"
import { IconButton } from "@/components/icon-button"
import { InvitationCard } from "@/components/mail/invitation-card"
import { SecureMessage } from "@/components/mail/secure-message"
import { LabelChips, LabelDialog, LabelMenu } from "@/components/mail/label-controls"
import { LaterMenu } from "@/components/mail/later-menu"
import { senderListQuery } from "@/components/settings/sender-lists"
import { SenderNotice, SenderVerification } from "@/components/mail/sender-notice"
import { RemoteImagesNotice } from "@/components/mail/remote-images-notice"
import { Spinner } from "@/components/spinner"
import { EmptyState, ErrorState } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { Button } from "@/components/ui/button"
import { api, apiUrl, errorMessage } from "@/lib/api"
import { formatAddress, formatBytes, formatDateTime, formatListDate } from "@/lib/format"
import { useI18n } from "@/lib/i18n"
import { folderIcons, folderLabel, messageDocument, openUnsubscribeLink, type ComposeMode } from "@/lib/mail"
import { foldersQuery, labelsQuery } from "@/lib/queries"
import type { Address, Folder, Label, Mailbox, Message, SenderEntry, UnsubscribeResult } from "@/lib/types"

function UnsubscribeBar({ message, mailboxId, folder, readOnly }: { message: Message; mailboxId: string; folder: string; readOnly?: boolean }) {
  const offer = message.unsubscribe
  const [done, setDone] = useState(false)
  const unsubscribe = useMutation({
    mutationFn: () => api<UnsubscribeResult>(`/mailboxes/${mailboxId}/messages/${message.uid}/unsubscribe`, { method: "POST", query: { folder } }),
    onSuccess: (result) => {
      if (result.url) {
        if (!openUnsubscribeLink(result.url)) toast.error("This unsubscribe link cannot be opened.")
        else toast.success("Opened the unsubscribe page.")
        return
      }
      setDone(true)
      toast.success(result.method === "mailto" ? "Unsubscribe request sent." : "Unsubscribed.")
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  if (!offer || (!offer.oneClick && !offer.link && !offer.mailto)) return null
  const mailtoOnly = offer.mailto && !offer.oneClick && !offer.link
  const detail = offer.oneClick ? "Aether sends the one-click unsubscribe request for you." : offer.link ? "Opens the sender's unsubscribe page." : "Sends an unsubscribe message from this mailbox."
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-muted px-4 py-3">
      <div className="min-w-0">
        <p className="text-sm font-semibold">This message includes an unsubscribe link.</p>
        <p className="text-sm text-fg-secondary">{detail}</p>
      </div>
      {mailtoOnly && readOnly ? (
        <p className="text-sm text-fg-tertiary">Unsubscribing sends a message, and this access cannot send.</p>
      ) : (
        <Button size="sm" variant="secondary" loading={unsubscribe.isPending} disabled={done} onClick={() => unsubscribe.mutate()}>
          {done ? "Unsubscribed" : "Unsubscribe"}
        </Button>
      )}
    </div>
  )
}

export function AddressLine({ label, addresses }: { label: string; addresses: Address[] }) {
  if (!addresses.length) return null
  return (
    <div className="flex gap-2 text-sm">
      <dt className="w-8 shrink-0 text-fg-tertiary">{label}</dt>
      <dd className="min-w-0 break-words text-fg-secondary">{addresses.map(formatAddress).join(", ")}</dd>
    </div>
  )
}

function MessageBody({ message: m, mailboxId, folder }: { message: Message; mailboxId: string; folder: string }) {
  const secure = !!(m.protection?.encrypted || m.protection?.signed)
  const attachments = m.attachments.filter((a) => !/pkcs7-signature|pgp-signature/i.test(a.contentType))
  const { t } = useI18n()
  return (
    <>
            {folder === "Drafts" && m.fileLinks && m.fileLinks.length > 0 && (
        <ul aria-label="Download links" className="flex flex-col gap-2">
          {m.fileLinks.map((link) => (
            <li key={link.id} className="flex min-h-10 max-w-full items-center gap-2 rounded-lg border border-strong px-3 py-2 text-sm sm:max-w-[420px]">
              <Link2 aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
              <span className="truncate">{link.filename}</span>
              <span className="shrink-0 text-xs text-fg-tertiary">{formatBytes(link.sizeBytes)}</span>
              <span className="shrink-0 text-xs text-fg-tertiary">{link.expiresAt ? `Expires ${formatListDate(link.expiresAt)}` : "Download link"}</span>
            </li>
          ))}
        </ul>
      )}
            {!m.protection?.encrypted && attachments.length > 0 && (
        <ul aria-label={t("mail.reading.attachments")} className="flex flex-wrap gap-2">
          {attachments.map((a) => (
            <li key={a.part}>
              <a
                href={apiUrl(`/mailboxes/${mailboxId}/messages/${m.uid}/attachments/${a.part}`, { folder })}
                download={a.filename}
                className="flex min-h-10 max-w-[280px] items-center gap-2 rounded-lg border border-strong px-3 py-2 text-sm transition-colors duration-160 hover:bg-muted"
              >
                <Download aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
                <span className="truncate">{a.filename || t("mail.reading.attachmentFallback")}</span>
                <span className="shrink-0 text-xs text-fg-tertiary">{formatBytes(a.size)}</span>
              </a>
            </li>
          ))}
        </ul>
      )}
      <RemoteImagesNotice message={m} mailboxId={mailboxId} folder={folder} />
      {secure ? (
        <SecureMessage message={m} mailboxId={mailboxId} folder={folder} />
      ) : m.html ? (
        <iframe
          title={t("mail.reading.messageBody")}
          sandbox="allow-popups allow-popups-to-escape-sandbox"
          srcDoc={messageDocument(m.html, m.remoteImages?.status === "proxied")}
          className="min-h-[400px] w-full flex-1 rounded-xl border border-border bg-white"
        />
      ) : (
        <pre className="font-sans text-base whitespace-pre-wrap break-words">{m.text}</pre>
      )}
    </>
  )
}

function messageQuery(mailboxId: string, folder: string, uid: number) {
  return {
    queryKey: ["message", mailboxId, folder, uid],
    queryFn: () => api<Message>(`/mailboxes/${mailboxId}/messages/${uid}`, { query: { folder } }),
  }
}

function Conversation({ message, earlier, mailboxId, folder, readOnly }: { message: Message; earlier: number[]; mailboxId: string; folder: string; readOnly?: boolean }) {
  const { t } = useI18n()
  const items = [...earlier.map((uid) => ({ folder, uid })), ...(message.related ?? [])]
  const queries = useQueries({ queries: items.map((i) => messageQuery(mailboxId, i.folder, i.uid)) })
  if (items.length === 0) return null
  const order = items.map((item, i) => ({ item, query: queries[i] }))
  order.sort((a, b) => (b.query.data ? Date.parse(b.query.data.date) : 0) - (a.query.data ? Date.parse(a.query.data.date) : 0))
  return (
    <section aria-label={t("mail.reading.moreInConversation")} className="flex flex-col gap-2 border-t border-border pt-4">
      <h3 className="text-label text-fg-tertiary">{t("mail.reading.moreInConversation")}</h3>
      {order.map(({ item, query }) => (
        <EarlierMessage key={`${item.folder}-${item.uid}`} query={query} mailboxId={mailboxId} folder={item.folder} showFolder={item.folder !== folder} readOnly={readOnly} />
      ))}
    </section>
  )
}

function EarlierMessage({ query, mailboxId, folder, showFolder, readOnly }: { query: UseQueryResult<Message>; mailboxId: string; folder: string; showFolder: boolean; readOnly?: boolean }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const m = query.data
  return (
    <div className="flex flex-col gap-4 rounded-xl border border-border">
      <button
        type="button"
        aria-expanded={open}
        disabled={!m}
        onClick={() => setOpen(!open)}
        className="flex min-h-12 items-center gap-3 rounded-xl px-4 py-2 text-left transition-colors duration-160 hover:bg-muted"
      >
        {m ? (
          <>
            {m.from[0] && <Avatar address={m.from[0].address} name={m.from[0].name} />}
            <span className="min-w-0 flex-1 truncate font-semibold">{m.from.map(formatAddress).join(", ") || t("mail.unknownSender")}</span>
            {showFolder && <span className="shrink-0 text-sm text-fg-tertiary">{folder === "INBOX" ? t("mail.folders.inbox") : folder}</span>}
            <time dateTime={m.date} className="shrink-0 text-sm text-fg-tertiary">
              {formatDateTime(m.date)}
            </time>
            <ChevronDown aria-hidden className={cn("size-4 shrink-0 text-fg-tertiary transition-transform duration-160", open && "rotate-180")} />
          </>
        ) : query.isError ? (
          <span className="text-sm text-fg-tertiary">{t("mail.reading.earlierLoadError")}</span>
        ) : (
          <Skeleton className="h-4 w-1/2" />
        )}
      </button>
      {open && m && (
        <div className="flex flex-col gap-4 px-4 pb-4">
          <UnsubscribeBar message={m} mailboxId={mailboxId} folder={folder} readOnly={readOnly} />
          <MessageBody message={m} mailboxId={mailboxId} folder={folder} />
        </div>
      )}
    </div>
  )
}

function MailboxFolders({ mailboxId, exclude, onPick }: { mailboxId: string; exclude?: string; onPick: (folder: Folder) => void }) {
  const { t } = useI18n()
  const folders = useQuery(foldersQuery(mailboxId))
  if (folders.isPending) {
    return (
      <DropdownMenuItem disabled>
        <Spinner />
        {t("mail.folders.loadingFolders")}
      </DropdownMenuItem>
    )
  }
  if (folders.isError) return <DropdownMenuItem disabled>{t("mail.folders.foldersLoadError")}</DropdownMenuItem>
  return folders.data
    .filter((f) => f.name !== exclude)
    .map((f) => {
      const Icon = folderIcons[f.specialUse]
      return (
        <DropdownMenuItem key={f.name} onSelect={() => onPick(f)}>
          <Icon aria-hidden className="text-fg-tertiary" />
          {folderLabel(f)}
        </DropdownMenuItem>
      )
    })
}

function FolderPicker({
  label,
  title,
  children,
  mailboxId,
  folder,
  mailboxes,
  disabled,
  onPick,
}: {
  label: string
  title: string
  children: React.ReactNode
  mailboxId: string
  folder: string
  mailboxes: Mailbox[]
  disabled: boolean
  onPick: (folder: Folder, mailbox?: Mailbox) => void
}) {
  const { t } = useI18n()
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button variant="quiet" size="icon" aria-label={label} disabled={disabled}>
              {children}
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">{title}</DropdownMenuLabel>
        <MailboxFolders mailboxId={mailboxId} exclude={folder} onPick={(f) => onPick(f)} />
        {mailboxes.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="px-3 py-2 text-xs font-semibold text-fg-tertiary">{t("mail.folders.otherMailboxes")}</DropdownMenuLabel>
            {mailboxes.map((m) => (
              <DropdownMenuSub key={m.id}>
                <DropdownMenuSubTrigger>
                  <AtSign aria-hidden className="text-fg-tertiary" />
                  <span className="truncate">{m.address}</span>
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent className="max-h-(--radix-dropdown-menu-content-available-height) w-56 overflow-y-auto">
                  <MailboxFolders mailboxId={m.id} onPick={(f) => onPick(f, m)} />
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            ))}
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function BlockMenu({ mailboxId, address, disabled }: { mailboxId: string; address?: string; disabled: boolean }) {
  const qc = useQueryClient()
  const domain = address?.split("@")[1]
  const block = useMutation({
    mutationFn: (value: string) => api<SenderEntry[]>(`/mailboxes/${mailboxId}/sender-lists`, { method: "POST", body: { action: "block", value } }),
    onSuccess: (list, value) => {
      qc.setQueryData(senderListQuery(mailboxId).queryKey, list)
      toast.success(`Blocked ${value}. New mail from them is deleted as it arrives.`)
    },
  })
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button variant="quiet" size="icon" aria-label="Block" disabled={disabled || !address || block.isPending}>
              {block.isPending ? <Spinner /> : <Ban />}
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>Block</TooltipContent>
      </Tooltip>
      {address && domain && (
        <DropdownMenuContent align="start" className="w-64">
          <DropdownMenuItem onSelect={() => block.mutate(address)}>
            <Ban aria-hidden className="text-fg-tertiary" />
            <span className="truncate">Block {address}</span>
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => block.mutate(`@${domain}`)}>
            <Globe aria-hidden className="text-fg-tertiary" />
            <span className="truncate">Block everyone at @{domain}</span>
          </DropdownMenuItem>
        </DropdownMenuContent>
      )}
    </DropdownMenu>
  )
}

export function ReadingPane({
  className,
  mailboxId,
  folder,
  mailboxes,
  query,
  selected,
  earlier,
  onBack,
  onReply,
  onFlags,
  onMove,
  onCopy,
  onDelete,
  onSpam,
  junk,
  onSnooze,
  onUnsnooze,
  deleting,
  readOnly,
  onLabels,
  placedIn,
}: {
  className?: string
  mailboxId: string
  folder: string
  mailboxes: Mailbox[]
  query: UseQueryResult<Message>
  selected: boolean
  earlier: number[]
  onBack: () => void
  onReply?: (mode: ComposeMode) => void
  onFlags: (flags: { seen?: boolean; flagged?: boolean }) => void
  onMove: (folder: Folder, mailbox?: Mailbox) => void
  onCopy: (folder: Folder, mailbox?: Mailbox) => void
  onDelete: () => void
  onSpam?: () => void
  junk?: boolean
  onSnooze: (until: Date) => void
  onUnsnooze?: () => void
  deleting: boolean
  readOnly?: boolean
  onLabels?: (label: Label, on: boolean) => void
  placedIn?: string
}) {
  const labels = useQuery({ ...labelsQuery(mailboxId), enabled: selected })
  const [creatingLabel, setCreatingLabel] = useState(false)
  const { t } = useI18n()

  if (!selected) {
    return (
      <section aria-label={t("mail.reading.sectionLabel")} className={cn("items-center justify-center", className)}>
        <EmptyState icon={Mail} title={t("mail.reading.noneSelectedTitle")} description={t("mail.reading.noneSelectedDescription")} />
      </section>
    )
  }

  const m = query.data

  return (
    <section aria-label={t("mail.reading.sectionLabel")} className={cn("min-h-0 flex-col", className)}>
      <div className="flex shrink-0 items-center gap-1 overflow-x-auto border-b border-border px-4 py-2">
        <IconButton label={t("mail.reading.backToMessages")} className="md:hidden" onClick={onBack}>
          <ArrowLeft />
        </IconButton>
        {onReply && (
          <>
            <IconButton label={t("mail.reading.reply")} disabled={!m} onClick={() => onReply("reply")}>
              <Reply />
            </IconButton>
            <IconButton label={t("mail.reading.replyAll")} disabled={!m} onClick={() => onReply("replyAll")}>
              <ReplyAll />
            </IconButton>
            <IconButton label={t("mail.reading.forward")} disabled={!m} onClick={() => onReply("forward")}>
              <Forward />
            </IconButton>
          </>
        )}
        {!readOnly && (
          <>
            {onReply && <span className="mx-1 h-6 w-px bg-border" />}
            <IconButton label={m?.seen ? t("mail.reading.markUnread") : t("mail.reading.markRead")} disabled={!m} onClick={() => m && onFlags({ seen: !m.seen })}>
              {m?.seen ? <Mail /> : <MailOpen />}
            </IconButton>
            <IconButton label={m?.flagged ? t("mail.reading.removeFlag") : t("mail.reading.flag")} aria-pressed={m?.flagged ?? false} disabled={!m} onClick={() => m && onFlags({ flagged: !m.flagged })}>
              <Flag className={cn(m?.flagged && "fill-current text-ember")} />
            </IconButton>
            {onLabels && (
              <LabelMenu labels={labels.data ?? []} keywords={m?.keywords} disabled={!m} onToggle={onLabels} onCreate={() => setCreatingLabel(true)} />
            )}
            <FolderPicker label={t("mail.reading.moveToFolder")} title={t("mail.reading.moveToTitle")} mailboxId={mailboxId} folder={folder} mailboxes={mailboxes} disabled={!m} onPick={onMove}>
              <FolderInput />
            </FolderPicker>
            <FolderPicker label={t("mail.reading.copyToFolder")} title={t("mail.reading.copyToTitle")} mailboxId={mailboxId} folder={folder} mailboxes={mailboxes} disabled={!m} onPick={onCopy}>
              <Copy />
            </FolderPicker>
            {onUnsnooze ? (
              <IconButton label={t("mail.reading.unsnooze")} disabled={!m || deleting} onClick={onUnsnooze}>
                <AlarmClockOff />
              </IconButton>
            ) : (
              <LaterMenu label={t("mail.reading.snooze")} submitLabel={t("mail.reading.snooze")} laterToday disabled={!m || deleting} onPick={onSnooze}>
                <Button variant="quiet" size="icon" aria-label={t("mail.reading.snooze")}>
                  <AlarmClock />
                </Button>
              </LaterMenu>
            )}
            <BlockMenu mailboxId={mailboxId} address={m?.from[0]?.address} disabled={!m} />
            {onSpam && (
              <IconButton label={junk ? "Not spam" : "Report spam"} disabled={!m || deleting} onClick={onSpam}>
                {junk ? <ShieldCheck /> : <OctagonAlert />}
              </IconButton>
            )}
            <IconButton label={t("mail.reading.delete")} disabled={!m || deleting} onClick={onDelete}>
              <Trash2 />
            </IconButton>
          </>
        )}
      </div>
      {query.isPending ? (
        <div aria-busy aria-label={t("mail.reading.loadingMessage")} className="flex flex-col gap-4 p-6">
          <Skeleton className="h-7 w-2/3" />
          <Skeleton className="h-4 w-1/2" />
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="mt-4 h-64 w-full" />
        </div>
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => query.refetch()} />
      ) : (
        m && (
          <article key={m.uid} className="animate-content flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-6">
            <header className="flex flex-col gap-3">
              <h2 className="text-h3 break-words">{m.subject || t("mail.noSubject")}</h2>
              {(placedIn || !!m.keywords?.length || !!m.flagColor) && (
                <div className="flex flex-wrap items-center gap-2">
                  {placedIn && <p className="text-sm text-fg-tertiary">In {placedIn}</p>}
                  <LabelChips labels={labels.data ?? []} keywords={m.keywords} flagColor={m.flagColor} />
                </div>
              )}
              <div className="flex items-center gap-3">
                {m.from[0] && <Avatar address={m.from[0].address} name={m.from[0].name} className="size-10 text-sm" />}
                <div className="flex min-w-0 flex-1 flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                  <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                    <p className="font-semibold break-all">{m.from.map(formatAddress).join(", ") || t("mail.unknownSender")}</p>
                    {m.sender?.external && (
                      <StatusBadge tone="neutral" icon={Globe}>
                        External
                      </StatusBadge>
                    )}
                  </div>
                  <time dateTime={m.date} className="text-sm text-fg-tertiary">
                    {formatDateTime(m.date)}
                  </time>
                </div>
              </div>
              <dl className="flex flex-col gap-1">
                <AddressLine label={t("mail.reading.to")} addresses={m.to} />
                <AddressLine label={t("mail.reading.cc")} addresses={m.cc} />
              </dl>
              {m.sender && m.from[0] && <SenderVerification sender={m.sender} from={m.from[0]} />}
              <UnsubscribeBar key={`${folder}-${m.uid}`} message={m} mailboxId={mailboxId} folder={folder} readOnly={readOnly} />
            </header>
            {m.sender && m.from[0] && <SenderNotice sender={m.sender} from={m.from[0]} />}
            {m.invitation && !readOnly && <InvitationCard key={`${m.uid}-${m.invitation.method}`} message={m} mailboxId={mailboxId} folder={folder} />}
            <MessageBody message={m} mailboxId={mailboxId} folder={folder} />
            <Conversation message={m} earlier={earlier} mailboxId={mailboxId} folder={folder} readOnly={readOnly} />
          </article>
        )
      )}
      {creatingLabel && <LabelDialog mailboxId={mailboxId} open onOpenChange={setCreatingLabel} />}
    </section>
  )
}

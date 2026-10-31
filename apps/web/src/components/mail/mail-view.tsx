"use client"

import { useInfiniteQuery, useMutation, useQueries, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query"
import { Inbox } from "lucide-react"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { Composer } from "@/components/mail/composer"
import { FolderPane } from "@/components/mail/folder-pane"
import { MessageList } from "@/components/mail/message-list"
import { ReadingPane } from "@/components/mail/reading-pane"
import { ScheduledList } from "@/components/mail/scheduled-list"
import { ShortcutsDialog } from "@/components/mail/shortcuts-dialog"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { useListView } from "@/hooks/use-list-view"
import { api } from "@/lib/api"
import { toRFC3339 } from "@/lib/calendar"
import { formatShortDateTime } from "@/lib/format"
import { buildDraft, folderLabel, snoozedFolder, type ComposeMode, type Draft } from "@/lib/mail"
import { foldersQuery, myMailboxesQuery } from "@/lib/queries"
import type { Folder, Mailbox, Message, MessagePage, MessageSummary, SavedDraft, ScheduledMessage, SnoozedMessage } from "@/lib/types"

const POLL = 30_000

type Flags = { seen?: boolean; flagged?: boolean }

function isTyping(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false
  return (
    target.isContentEditable ||
    ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) ||
    !!target.closest('[role="dialog"],[role="alertdialog"],[role="menu"],[role="listbox"]')
  )
}

export function MailView() {
  const qc = useQueryClient()
  const mailboxes = useQuery(myMailboxesQuery)
  const own = mailboxes.data?.filter((m) => m.kind !== "shared") ?? []
  const shared = mailboxes.data?.filter((m) => m.kind === "shared") ?? []
  const [primaryId, setPrimaryId] = useState<string | null>(null)
  const primary = own.find((m) => m.id === primaryId) ?? own[0]
  const sections = primary ? [primary, ...shared] : shared
  const [mailboxId, setMailboxId] = useState<string | null>(null)
  const mailbox = sections.find((m) => m.id === mailboxId) ?? sections[0]
  const readOnly = mailbox?.access === "read"
  const mbx = mailbox?.id ?? ""
  const identities = mailboxes.data?.filter((m) => m.access === "owner" || m.sendAs || m.sendOnBehalf) ?? []
  const from = identities.find((m) => m.id === mbx) ?? identities[0]

  const folderQueries = useQueries({ queries: sections.map((m) => ({ ...foldersQuery(m.id), refetchInterval: POLL })) })
  const trees = sections.map((m, i) => ({ mailbox: m, folders: folderQueries[i] }))
  const folders = trees.find((t) => t.mailbox.id === mbx)?.folders
  const [folderName, setFolderName] = useState<string | null>(null)
  const folder = folderName ?? folders?.data?.find((f) => f.specialUse === "inbox")?.name ?? "INBOX"

  const scheduled = useQuery({
    queryKey: ["scheduled", mbx],
    queryFn: () => api<ScheduledMessage[]>(`/mailboxes/${mbx}/scheduled`),
    enabled: !!mailbox,
    refetchInterval: POLL,
  })
  const [scheduledView, setScheduledView] = useState(false)
  const showScheduled = scheduledView && !!scheduled.data?.length

  const snoozed = useQuery({
    queryKey: ["snoozed", mbx],
    queryFn: () => api<SnoozedMessage[]>(`/mailboxes/${mbx}/snoozed`),
    enabled: !!mailbox && folder === snoozedFolder,
  })
  const wakeTimes = new Map(snoozed.data?.map((s) => [s.uid, s.until]))

  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 300)

  const isDrafts = folders?.data?.find((f) => f.name === folder)?.specialUse === "drafts"
  const [view, setView] = useListView(`${mbx}/${folder}`)
  const listKey = ["messages", mbx, folder]
  const messages = useInfiniteQuery({
    queryKey: [...listKey, q, view],
    queryFn: ({ pageParam }) =>
      api<MessagePage>(`/mailboxes/${mbx}/messages`, {
        query: { folder, q, cursor: pageParam, limit: 50, threads: isDrafts ? undefined : "true", sort: view.sort, order: view.order, ...Object.fromEntries(view.filters.map((f) => [f, "true"])) },
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
    enabled: !!folders?.isSuccess,
    refetchInterval: POLL,
  })
  const summaries = messages.data?.pages.flatMap((p) => p.messages) ?? []

  const [selectedUid, setSelectedUid] = useState<number | null>(null)
  const [showFolders, setShowFolders] = useState(false)
  const [compose, setCompose] = useState<{ key: number; draft: Draft } | null>(null)
  const [helpOpen, setHelpOpen] = useState(false)
  const searchRef = useRef<HTMLInputElement>(null)
  const router = useRouter()
  const composeTo = useSearchParams().get("compose")
  const [composedTo, setComposedTo] = useState<string | null>(null)
  if (mailbox && composeTo !== composedTo) {
    setComposedTo(composeTo)
    if (composeTo && from) setCompose({ key: (compose?.key ?? 0) + 1, draft: { ...buildDraft("new", undefined, mailbox.address, folder), to: [{ address: composeTo }] } })
  }
  useEffect(() => {
    if (composeTo && composeTo === composedTo) router.replace("/mail", { scroll: false })
  }, [composeTo, composedTo, router])

  const message = useQuery({
    queryKey: ["message", mbx, folder, selectedUid],
    queryFn: () => api<Message>(`/mailboxes/${mbx}/messages/${selectedUid}`, { query: { folder } }),
    enabled: selectedUid !== null,
  })

  function patchCaches(uid: number, patch: Partial<MessageSummary> | null) {
    qc.setQueriesData<InfiniteData<MessagePage>>({ queryKey: listKey }, (data) =>
      data && {
        ...data,
        pages: data.pages.map((page) => ({
          ...page,
          messages: patch
            ? page.messages.map((m) => (m.uid === uid ? { ...m, ...patch } : m))
            : page.messages.filter((m) => m.uid !== uid),
        })),
      }
    )
    if (patch) qc.setQueryData<Message>(["message", mbx, folder, uid], (m) => m && { ...m, ...patch })
  }

  const setFlags = useMutation({
    mutationFn: ({ uid, flags }: { uid: number; flags: Flags }) =>
      api(`/mailboxes/${mbx}/messages/${uid}`, { method: "PATCH", query: { folder }, body: flags }),
    onMutate: ({ uid, flags }) => patchCaches(uid, flags),
    onError: (_e, { uid, flags }) =>
      patchCaches(uid, Object.fromEntries(Object.entries(flags).map(([k, v]) => [k, !v]))),
    onSettled: (_d, _e, { uid }) => {
      qc.invalidateQueries({ queryKey: ["folders", mbx] })
      qc.invalidateQueries({ queryKey: ["message", mbx, folder, uid] })
    },
  })

  const remove = useMutation({
    mutationFn: ({ uid, to, target, until, unsnooze }: { uid: number; to?: Folder; target?: Mailbox; until?: Date; unsnooze?: boolean }) =>
      unsnooze
        ? api(`/mailboxes/${mbx}/snoozed/${uid}`, { method: "DELETE" })
        : until
          ? api(`/mailboxes/${mbx}/messages/${uid}/snooze`, { method: "POST", query: { folder }, body: { until: toRFC3339(until) } })
          : to
            ? api(`/mailboxes/${mbx}/messages/${uid}/move`, { method: "POST", query: { folder }, body: { to: to.name, mailboxId: target?.id } })
            : api(`/mailboxes/${mbx}/messages/${uid}`, { method: "DELETE", query: { folder } }),
    onSuccess: (_d, { uid, to, target, until, unsnooze }) => {
      const index = summaries.findIndex((m) => m.uid === uid)
      const next = summaries[index + 1] ?? summaries[index - 1]
      patchCaches(uid, null)
      if (selectedUid === uid) setSelectedUid(next?.uid ?? null)
      toast.success(
        unsnooze
          ? `Moved back to ${snoozed.data?.find((s) => s.uid === uid)?.returnFolder ?? "its folder"}`
          : until
            ? `Snoozed until ${formatShortDateTime(until)}`
            : to
              ? `Moved to ${folderLabel(to)}${target ? ` in ${target.address}` : ""}`
              : "Message deleted"
      )
      qc.invalidateQueries({ queryKey: ["folders"] })
      qc.invalidateQueries({ queryKey: ["messages"] })
      qc.invalidateQueries({ queryKey: ["snoozed", mbx] })
    },
  })

  const copy = useMutation({
    mutationFn: ({ uid, to, target }: { uid: number; to: Folder; target?: Mailbox }) =>
      api(`/mailboxes/${mbx}/messages/${uid}/copy`, { method: "POST", query: { folder }, body: { to: to.name, mailboxId: target?.id } }),
    onSuccess: (_d, { to, target }) => {
      toast.success(`Copied to ${folderLabel(to)}${target ? ` in ${target.address}` : ""}`)
      qc.invalidateQueries({ queryKey: ["folders", target?.id ?? mbx] })
      qc.invalidateQueries({ queryKey: ["messages", target?.id ?? mbx] })
    },
  })

  async function openDraft(uid: number, draftFolder: string) {
    if (!mailbox || from?.id !== mbx) return
    const saved = await qc.fetchQuery({
      queryKey: ["message", mbx, draftFolder, uid],
      queryFn: () => api<Message>(`/mailboxes/${mbx}/messages/${uid}`, { query: { folder: draftFolder } }),
    })
    const draft = buildDraft("draft", saved, mailbox.address, draftFolder)
    setCompose((current) => ({ key: (current?.key ?? 0) + 1, draft }))
  }

  const cancelScheduled = useMutation({
    mutationFn: (id: string) => api<Omit<SavedDraft, "attachments">>(`/mailboxes/${mbx}/scheduled/${id}`, { method: "DELETE" }),
    onSuccess: async (draft) => {
      qc.invalidateQueries({ queryKey: ["scheduled", mbx] })
      qc.invalidateQueries({ queryKey: ["folders", mbx] })
      qc.invalidateQueries({ queryKey: ["messages", mbx] })
      await openDraft(draft.uid, draft.folder)
    },
  })

  async function open(summary: MessageSummary) {
    if (isDrafts && from?.id === mbx) return openDraft(summary.uid, folder)
    setSelectedUid(summary.uid)
    if (!summary.seen && !readOnly) for (const uid of summary.threadUids ?? [summary.uid]) setFlags.mutate({ uid, flags: { seen: true } })
  }

  function step(delta: number) {
    if (!summaries.length) return
    const index = summaries.findIndex((m) => m.uid === selectedUid)
    const target = summaries[index === -1 ? 0 : Math.min(Math.max(index + delta, 0), summaries.length - 1)]
    if (target) open(target)
  }

  function startCompose(mode: ComposeMode) {
    if (!mailbox || !from) return
    if (mode !== "new" && !message.data) return
    const draft = buildDraft(mode, message.data, mailbox.address, folder)
    if (from.id !== mbx && draft.forwardFrom) toast.info(`Forwarded attachments can't be sent from ${from.address}, so they were left out.`)
    setCompose((current) => ({ key: (current?.key ?? 0) + 1, draft }))
  }

  function selectFolder(id: string, name: string) {
    setMailboxId(id)
    setFolderName(name)
    setScheduledView(false)
    setSelectedUid(null)
    setShowFolders(false)
  }

  function selectMailbox(id: string) {
    setMailboxId(id)
    setScheduledView(false)
    setFolderName(null)
    setSelectedUid(null)
  }

  const shortcuts = useRef<(e: KeyboardEvent) => void>(() => {})
  useEffect(() => {
    shortcuts.current = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || compose || helpOpen || isTyping(e.target)) return
      const uid = selectedUid
      const actions: Record<string, () => void> = {
        c: () => startCompose("new"),
        j: () => step(1),
        k: () => step(-1),
        r: () => startCompose("reply"),
        a: () => startCompose("replyAll"),
        f: () => startCompose("forward"),
        e: () => uid !== null && remove.mutate({ uid }),
        "#": () => uid !== null && remove.mutate({ uid }),
        "/": () => searchRef.current?.focus(),
        "?": () => setHelpOpen(true),
        Escape: () => setSelectedUid(null),
      }
      const action = actions[e.key]
      if (!action || (readOnly && "#e".includes(e.key)) || (!from && "craf".includes(e.key))) return
      e.preventDefault()
      action()
    }
  })
  useEffect(() => {
    const handler = (e: KeyboardEvent) => shortcuts.current(e)
    window.addEventListener("keydown", handler)
    return () => window.removeEventListener("keydown", handler)
  }, [])

  if (mailboxes.isPending) {
    return (
      <div className="flex h-full gap-6 p-6">
        <RowsSkeleton rows={6} className="hidden w-60 lg:flex" />
        <RowsSkeleton rows={8} className="flex-1 md:max-w-[400px]" />
      </div>
    )
  }
  if (mailboxes.isError) return <ErrorState error={mailboxes.error} onRetry={() => mailboxes.refetch()} className="h-full" />
  if (!mailbox) {
    return (
      <EmptyState
        icon={Inbox}
        title="You do not have a mailbox yet"
        description="An administrator creates mailboxes in Administration. Once one is assigned to your account it appears here."
        className="h-full"
      />
    )
  }

  const reading = selectedUid !== null

  return (
    <div className="flex h-full min-h-0">
      <FolderPane
        className={cn("w-full shrink-0 md:w-66 lg:flex", showFolders && !reading ? "flex" : "hidden")}
        own={own}
        primary={trees.find((t) => t.mailbox.id === primary?.id)}
        onPrimaryChange={(id) => {
          setPrimaryId(id)
          selectMailbox(id)
        }}
        shared={trees.filter((t) => t.mailbox.id !== primary?.id)}
        active={{ mailboxId: mbx, folder }}
        onFolderChange={selectFolder}
        scheduled={scheduled.data?.length}
        scheduledActive={showScheduled}
        onScheduled={() => {
          setScheduledView(true)
          setSelectedUid(null)
          setShowFolders(false)
        }}
        onCompose={from ? () => startCompose("new") : undefined}
        onDropMessage={readOnly ? undefined : (uid, to, target) => remove.mutate({ uid, to, target: target === mbx ? undefined : mailboxes.data.find((m) => m.id === target) })}
      />
      {showScheduled ? (
        <ScheduledList
          className={cn("w-full shrink-0 md:flex md:w-[360px] xl:w-[400px]", showFolders ? "hidden" : "flex")}
          messages={scheduled.data ?? []}
          cancelling={cancelScheduled.isPending ? cancelScheduled.variables : undefined}
          onCancel={(id) => cancelScheduled.mutate(id)}
          onShowFolders={() => setShowFolders(true)}
        />
      ) : (
        <MessageList
          className={cn("w-full shrink-0 md:flex md:w-[360px] xl:w-[400px]", showFolders || reading ? "hidden" : "flex")}
          folder={folders?.data?.find((f) => f.name === folder) ?? { name: folder, specialUse: "" }}
          search={search}
          onSearchChange={setSearch}
          searchRef={searchRef}
          q={q}
          query={messages}
          summaries={summaries}
          selectedUid={selectedUid}
          onOpen={open}
          onShowFolders={() => setShowFolders(true)}
          onCompose={from ? () => startCompose("new") : undefined}
          onHelp={() => setHelpOpen(true)}
          wakeTimes={folder === snoozedFolder ? wakeTimes : undefined}
          view={view}
          onViewChange={setView}
          draggable={!readOnly}
        />
      )}
      <ReadingPane
        className={cn("min-w-0 flex-1 md:flex", reading ? "flex" : "hidden")}
        mailboxId={mbx}
        folder={folder}
        mailboxes={mailboxes.data.filter((m) => m.id !== mbx && m.access !== "read")}
        query={message}
        selected={selectedUid !== null}
        earlier={summaries.find((m) => m.uid === selectedUid)?.threadUids?.slice(1) ?? []}
        onBack={() => setSelectedUid(null)}
        onReply={from ? startCompose : undefined}
        onFlags={(flags) => selectedUid !== null && setFlags.mutate({ uid: selectedUid, flags })}
        onMove={(to, target) => selectedUid !== null && remove.mutate({ uid: selectedUid, to, target })}
        onCopy={(to, target) => selectedUid !== null && copy.mutate({ uid: selectedUid, to, target })}
        onDelete={() => selectedUid !== null && remove.mutate({ uid: selectedUid })}
        onSnooze={(until) => selectedUid !== null && remove.mutate({ uid: selectedUid, until })}
        onUnsnooze={folder === snoozedFolder ? () => selectedUid !== null && remove.mutate({ uid: selectedUid, unsnooze: true }) : undefined}
        deleting={remove.isPending}
        readOnly={readOnly}
      />
      {compose && from && (
        <Composer
          key={compose.key}
          identities={identities}
          initial={from}
          draft={from.id === mbx ? compose.draft : { ...compose.draft, draftUid: undefined, forwardFrom: undefined }}
          onClose={() => setCompose(null)}
        />
      )}
      <ShortcutsDialog open={helpOpen} onOpenChange={setHelpOpen} />
    </div>
  )
}

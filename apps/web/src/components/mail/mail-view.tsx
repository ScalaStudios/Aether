"use client"

import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query"
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
import { api } from "@/lib/api"
import { toRFC3339 } from "@/lib/calendar"
import { formatShortDateTime } from "@/lib/format"
import { buildDraft, type ComposeMode, type Draft } from "@/lib/mail"
import { foldersQuery, myMailboxesQuery } from "@/lib/queries"
import type { Message, MessagePage, MessageSummary, SavedDraft, ScheduledMessage, SnoozedMessage } from "@/lib/types"

const POLL = 30_000
const SNOOZED = "Snoozed"

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
  const [mailboxId, setMailboxId] = useState<string | null>(null)
  const mailbox = mailboxes.data?.find((m) => m.id === mailboxId) ?? mailboxes.data?.[0]
  const readOnly = mailbox?.access === "read"
  const mbx = mailbox?.id ?? ""

  const folders = useQuery({
    ...foldersQuery(mbx),
    enabled: !!mailbox,
    refetchInterval: POLL,
  })
  const [folderName, setFolderName] = useState<string | null>(null)
  const folder = folderName ?? folders.data?.find((f) => f.specialUse === "inbox")?.name ?? "INBOX"

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
    enabled: !!mailbox && folder === SNOOZED,
  })
  const wakeTimes = new Map(snoozed.data?.map((s) => [s.uid, s.until]))

  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 300)

  const isDrafts = folders.data?.find((f) => f.name === folder)?.specialUse === "drafts"
  const listKey = ["messages", mbx, folder]
  const messages = useInfiniteQuery({
    queryKey: [...listKey, q],
    queryFn: ({ pageParam }) =>
      api<MessagePage>(`/mailboxes/${mbx}/messages`, { query: { folder, q, before: pageParam, limit: 50, threads: isDrafts ? undefined : "true" } }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
    enabled: !!mailbox && folders.isSuccess,
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
    if (composeTo) setCompose({ key: (compose?.key ?? 0) + 1, draft: { ...buildDraft("new", undefined, mailbox.address, folder), to: [{ address: composeTo }] } })
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
    mutationFn: ({ uid, to, until, unsnooze }: { uid: number; to?: string; until?: Date; unsnooze?: boolean }) =>
      unsnooze
        ? api(`/mailboxes/${mbx}/snoozed/${uid}`, { method: "DELETE" })
        : until
          ? api(`/mailboxes/${mbx}/messages/${uid}/snooze`, { method: "POST", query: { folder }, body: { until: toRFC3339(until) } })
          : to
            ? api(`/mailboxes/${mbx}/messages/${uid}/move`, { method: "POST", query: { folder }, body: { to } })
            : api(`/mailboxes/${mbx}/messages/${uid}`, { method: "DELETE", query: { folder } }),
    onSuccess: (_d, { uid, to, until, unsnooze }) => {
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
              ? `Moved to ${to}`
              : "Message deleted"
      )
      qc.invalidateQueries({ queryKey: ["folders", mbx] })
      qc.invalidateQueries({ queryKey: ["messages", mbx] })
      qc.invalidateQueries({ queryKey: ["snoozed", mbx] })
    },
  })

  async function openDraft(uid: number, draftFolder: string) {
    if (!mailbox) return
    const saved = await qc.fetchQuery({
      queryKey: ["message", mbx, draftFolder, uid],
      queryFn: () => api<Message>(`/mailboxes/${mbx}/messages/${uid}`, { query: { folder: draftFolder } }),
    })
    setCompose({ key: Date.now(), draft: buildDraft("draft", saved, mailbox.address, draftFolder) })
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
    if (isDrafts) return openDraft(summary.uid, folder)
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
    if (!mailbox) return
    if (mode !== "new" && !message.data) return
    setCompose({ key: Date.now(), draft: buildDraft(mode, message.data, mailbox.address, folder) })
  }

  function selectFolder(name: string) {
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
      if (!action || (readOnly && "craf#e".includes(e.key))) return
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
        mailboxes={mailboxes.data}
        mailbox={mailbox}
        onMailboxChange={selectMailbox}
        folders={folders}
        folder={folder}
        onFolderChange={selectFolder}
        scheduled={scheduled.data?.length}
        scheduledActive={showScheduled}
        onScheduled={() => {
          setScheduledView(true)
          setSelectedUid(null)
          setShowFolders(false)
        }}
        onCompose={readOnly ? undefined : () => startCompose("new")}
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
          folder={folders.data?.find((f) => f.name === folder) ?? { name: folder, specialUse: "" }}
          search={search}
          onSearchChange={setSearch}
          searchRef={searchRef}
          q={q}
          query={messages}
          summaries={summaries}
          selectedUid={selectedUid}
          onOpen={open}
          onShowFolders={() => setShowFolders(true)}
          onCompose={readOnly ? undefined : () => startCompose("new")}
          onHelp={() => setHelpOpen(true)}
          wakeTimes={folder === SNOOZED ? wakeTimes : undefined}
        />
      )}
      <ReadingPane
        className={cn("min-w-0 flex-1 md:flex", reading ? "flex" : "hidden")}
        mailboxId={mbx}
        folder={folder}
        folders={folders.data ?? []}
        query={message}
        selected={selectedUid !== null}
        earlier={summaries.find((m) => m.uid === selectedUid)?.threadUids?.slice(1) ?? []}
        onBack={() => setSelectedUid(null)}
        onReply={startCompose}
        onFlags={(flags) => selectedUid !== null && setFlags.mutate({ uid: selectedUid, flags })}
        onMove={(to) => selectedUid !== null && remove.mutate({ uid: selectedUid, to })}
        onDelete={() => selectedUid !== null && remove.mutate({ uid: selectedUid })}
        onSnooze={(until) => selectedUid !== null && remove.mutate({ uid: selectedUid, until })}
        onUnsnooze={folder === SNOOZED ? () => selectedUid !== null && remove.mutate({ uid: selectedUid, unsnooze: true }) : undefined}
        deleting={remove.isPending}
        readOnly={readOnly}
      />
      {compose && <Composer key={compose.key} mailbox={mailbox} draft={compose.draft} onClose={() => setCompose(null)} />}
      <ShortcutsDialog open={helpOpen} onOpenChange={setHelpOpen} />
    </div>
  )
}

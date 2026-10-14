"use client"

import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query"
import { Inbox } from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { Composer } from "@/components/mail/composer"
import { FolderPane } from "@/components/mail/folder-pane"
import { MessageList } from "@/components/mail/message-list"
import { ReadingPane } from "@/components/mail/reading-pane"
import { ShortcutsDialog } from "@/components/mail/shortcuts-dialog"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { api } from "@/lib/api"
import { buildDraft, type ComposeMode, type Draft } from "@/lib/mail"
import { myMailboxesQuery } from "@/lib/queries"
import type { Folder, Message, MessagePage, MessageSummary } from "@/lib/types"

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
  const [mailboxId, setMailboxId] = useState<string | null>(null)
  const mailbox = mailboxes.data?.find((m) => m.id === mailboxId) ?? mailboxes.data?.[0]
  const mbx = mailbox?.id ?? ""

  const folders = useQuery({
    queryKey: ["folders", mbx],
    queryFn: () => api<Folder[]>(`/mailboxes/${mbx}/folders`),
    enabled: !!mailbox,
    refetchInterval: POLL,
  })
  const [folderName, setFolderName] = useState<string | null>(null)
  const folder = folderName ?? folders.data?.find((f) => f.specialUse === "inbox")?.name ?? "INBOX"

  const [search, setSearch] = useState("")
  const [q, setQ] = useState("")
  useEffect(() => {
    const timer = setTimeout(() => setQ(search.trim()), 300)
    return () => clearTimeout(timer)
  }, [search])

  const listKey = ["messages", mbx, folder]
  const messages = useInfiniteQuery({
    queryKey: [...listKey, q],
    queryFn: ({ pageParam }) =>
      api<MessagePage>(`/mailboxes/${mbx}/messages`, { query: { folder, q, before: pageParam, limit: 50 } }),
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
    mutationFn: ({ uid, to }: { uid: number; to?: string }) =>
      to
        ? api(`/mailboxes/${mbx}/messages/${uid}/move`, { method: "POST", query: { folder }, body: { to } })
        : api(`/mailboxes/${mbx}/messages/${uid}`, { method: "DELETE", query: { folder } }),
    onSuccess: (_d, { uid, to }) => {
      const index = summaries.findIndex((m) => m.uid === uid)
      const next = summaries[index + 1] ?? summaries[index - 1]
      patchCaches(uid, null)
      if (selectedUid === uid) setSelectedUid(next?.uid ?? null)
      toast.success(to ? `Moved to ${to}` : "Message deleted")
      qc.invalidateQueries({ queryKey: ["folders", mbx] })
      qc.invalidateQueries({ queryKey: ["messages", mbx] })
    },
  })

  async function open(summary: MessageSummary) {
    if (mailbox && folders.data?.find((f) => f.name === folder)?.specialUse === "drafts") {
      const saved = await qc.fetchQuery({
        queryKey: ["message", mbx, folder, summary.uid],
        queryFn: () => api<Message>(`/mailboxes/${mbx}/messages/${summary.uid}`, { query: { folder } }),
      })
      setCompose({ key: Date.now(), draft: buildDraft("draft", saved, mailbox.address, folder) })
      return
    }
    setSelectedUid(summary.uid)
    if (!summary.seen) setFlags.mutate({ uid: summary.uid, flags: { seen: true } })
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
    setSelectedUid(null)
    setShowFolders(false)
  }

  function selectMailbox(id: string) {
    setMailboxId(id)
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
      if (!action) return
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
        onCompose={() => startCompose("new")}
      />
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
        onCompose={() => startCompose("new")}
        onHelp={() => setHelpOpen(true)}
      />
      <ReadingPane
        className={cn("min-w-0 flex-1 md:flex", reading ? "flex" : "hidden")}
        mailboxId={mbx}
        folder={folder}
        folders={folders.data ?? []}
        query={message}
        selected={selectedUid !== null}
        onBack={() => setSelectedUid(null)}
        onReply={startCompose}
        onFlags={(flags) => selectedUid !== null && setFlags.mutate({ uid: selectedUid, flags })}
        onMove={(to) => selectedUid !== null && remove.mutate({ uid: selectedUid, to })}
        onDelete={() => selectedUid !== null && remove.mutate({ uid: selectedUid })}
        deleting={remove.isPending}
      />
      {compose && <Composer key={compose.key} mailbox={mailbox} draft={compose.draft} onClose={() => setCompose(null)} />}
      <ShortcutsDialog open={helpOpen} onOpenChange={setHelpOpen} />
    </div>
  )
}

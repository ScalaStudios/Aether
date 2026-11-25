"use client"

import { useInfiniteQuery, useMutation, useQueries, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query"
import { Inbox } from "lucide-react"
import { useRouter, useSearchParams } from "next/navigation"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { cn } from "cn"

import { useLiveConnected } from "@/components/live-events"
import { Composer } from "@/components/mail/composer"
import { FolderPane } from "@/components/mail/folder-pane"
import { SearchDialog } from "@/components/mail/label-controls"
import { MessageList } from "@/components/mail/message-list"
import { QuarantineList } from "@/components/mail/quarantine-list"
import { QuarantinePane } from "@/components/mail/quarantine-pane"
import { ReadingPane } from "@/components/mail/reading-pane"
import { ScheduledList } from "@/components/mail/scheduled-list"
import { ShortcutsDialog } from "@/components/mail/shortcuts-dialog"
import { StorageBanner } from "@/components/mail/storage-banner"
import { UndoSendBar, isUndoHold, useNow, type UndoHold } from "@/components/mail/undo-send-bar"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { useDebouncedValue } from "@/hooks/use-debounced-value"
import { useListView } from "@/hooks/use-list-view"
import { api, errorMessage } from "@/lib/api"
import { toRFC3339 } from "@/lib/calendar"
import { formatShortDateTime } from "@/lib/format"
import { buildDraft, composeIdentity, defaultListView, describeSearch, filtersFromFlags, folderLabel, retentionNotice, savedSearchBody, senderIdentities, snoozedFolder, type ComposeMode, type Draft, type ListView, type SenderIdentity } from "@/lib/mail"
import { foldersQuery, labelsQuery, myMailboxesQuery, savedSearchesQuery } from "@/lib/queries"
import type { ContactGroup, Folder, Label, Mailbox, MailboxRetention, Message, MessagePage, MessageSummary, QuarantinePage, SavedSearch, ScheduledMessage, SnoozedMessage } from "@/lib/types"

const POLL = 30_000

type Flags = { seen?: boolean; flagged?: boolean }
type Browse = { kind: "label" | "search"; id: string }
type Selection = { uid: number; folder: string }

function sameFilters(left: ListView["filters"], right: ListView["filters"]) {
  return left.length === right.length && left.every((item) => right.includes(item))
}

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
  const live = useLiveConnected()
  const refresh = live ? 120_000 : POLL
  const mailboxes = useQuery({ ...myMailboxesQuery, select: (list) => list.filter((m) => m.protocols.includes("webmail")) })
  const own = mailboxes.data?.filter((m) => m.kind !== "shared") ?? []
  const shared = mailboxes.data?.filter((m) => m.kind === "shared") ?? []
  const [primaryId, setPrimaryId] = useState<string | null>(null)
  const primary = own.find((m) => m.id === primaryId) ?? own[0]
  const sections = primary ? [primary, ...shared] : shared
  const [mailboxId, setMailboxId] = useState<string | null>(null)
  const mailbox = sections.find((m) => m.id === mailboxId) ?? sections[0]
  const readOnly = mailbox?.access === "read"
  const mbx = mailbox?.id ?? ""
  const identities = senderIdentities(mailboxes.data ?? [], mbx)
  const from = identities.find((i) => i.from.address === mailbox?.address) ?? identities[0]

  const folderQueries = useQueries({ queries: sections.map((m) => ({ ...foldersQuery(m.id), refetchInterval: refresh })) })
  const trees = sections.map((m, i) => ({ mailbox: m, folders: folderQueries[i] }))
  const folders = trees.find((t) => t.mailbox.id === mbx)?.folders
  const [folderName, setFolderName] = useState<string | null>(null)
  const folder = folderName ?? folders?.data?.find((f) => f.specialUse === "inbox")?.name ?? "INBOX"

  const retention = useQuery({
    queryKey: ["retention", mbx],
    queryFn: () => api<MailboxRetention>(`/mailboxes/${mbx}/retention`),
    enabled: !!mailbox,
  })

  const scheduled = useQuery({
    queryKey: ["scheduled", mbx],
    queryFn: () => api<ScheduledMessage[]>(`/mailboxes/${mbx}/scheduled`),
    enabled: !!mailbox,
    refetchInterval: (query) => (query.state.data?.some(isUndoHold) ? 2_000 : refresh),
  })
  const extraIds = [...new Set(identities.map((identity) => identity.mailbox.id))].filter((id) => id !== mbx)
  const extraScheduled = useQueries({
    queries: extraIds.map((id) => ({
      queryKey: ["scheduled", id],
      queryFn: () => api<ScheduledMessage[]>(`/mailboxes/${id}/scheduled`),
      refetchInterval: (query: { state: { data?: ScheduledMessage[] } }) => (query.state.data?.some(isUndoHold) ? 2_000 : POLL),
    })),
  })
  const [scheduledView, setScheduledView] = useState(false)
  const scheduledItems = (scheduled.data ?? []).filter((message) => !isUndoHold(message))
  const showScheduled = scheduledView && scheduledItems.length > 0

  const quarantine = useInfiniteQuery({
    queryKey: ["quarantine", mbx],
    queryFn: ({ pageParam }) => api<QuarantinePage>(`/mailboxes/${mbx}/quarantine`, { query: { before: pageParam } }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
    enabled: !!mailbox,
    refetchInterval: POLL,
  })
  const held = quarantine.data?.pages.flatMap((p) => p.items) ?? []
  const [quarantineView, setQuarantineView] = useState(false)
  const [quarantineId, setQuarantineId] = useState<string | null>(null)

  const snoozed = useQuery({
    queryKey: ["snoozed", mbx],
    queryFn: () => api<SnoozedMessage[]>(`/mailboxes/${mbx}/snoozed`),
    enabled: !!mailbox && folder === snoozedFolder,
  })
  const wakeTimes = new Map(snoozed.data?.map((s) => [s.uid, s.until]))

  const [search, setSearch] = useState("")
  const q = useDebouncedValue(search.trim(), 300)

  const [browse, setBrowse] = useState<Browse | null>(null)
  const [heldSearch, setHeldSearch] = useState("")
  const [searchView, setSearchView] = useState<ListView>(defaultListView)
  const [savingSearch, setSavingSearch] = useState(false)
  const [searchError, setSearchError] = useState("")
  const labels = useQuery({ ...labelsQuery(mbx), enabled: !!mailbox })
  const searches = useQuery({ ...savedSearchesQuery(mbx), enabled: !!mailbox })
  const activeSearch = browse?.kind === "search" ? searches.data?.find((item) => item.id === browse.id) : undefined
  const activeLabel = browse?.kind === "label" ? labels.data?.find((item) => item.id === browse.id) : undefined
  const [folderView, setFolderView] = useListView(`${mbx}/${folder}`)
  const [labelView, setLabelView] = useListView(`${mbx}/label/${browse?.kind === "label" ? browse.id : "none"}`)
  const view = browse?.kind === "search" ? searchView : browse?.kind === "label" ? labelView : folderView
  const searchDirty = !!(activeSearch && (search.trim() !== activeSearch.query || (view.labelId || "") !== (activeSearch.labelId || "") || !sameFilters(view.filters, filtersFromFlags(activeSearch))))
  const spanning = browse?.kind === "label" || (!!activeSearch && activeSearch.allFolders)
  const scopeFolder = activeSearch && !activeSearch.allFolders ? activeSearch.folder : folder
  const specialUse = folders?.data?.find((f) => f.name === scopeFolder)?.specialUse
  const isDrafts = !spanning && specialUse === "drafts"
  const isJunk = !spanning && specialUse === "junk"
  const spamTarget = spanning ? undefined : folders?.data?.find((f) => f.specialUse === (isJunk ? "inbox" : "junk"))
  const cleanSaved = browse?.kind === "search" && !!activeSearch && !searchDirty
  const listKey = ["messages", mbx, browse?.kind ?? "folder", browse?.id ?? scopeFolder]
  const messages = useInfiniteQuery({
    queryKey: [...listKey, cleanSaved ? "saved" : q, view, browse?.kind === "search" && !activeSearch ? "missing" : "ready"],
    queryFn: ({ pageParam }): Promise<MessagePage> =>
      browse?.kind === "search" && !activeSearch
        ? Promise.resolve({ messages: [], total: 0 })
        : api<MessagePage>(`/mailboxes/${mbx}/messages`, {
            query: cleanSaved
              ? { saved: activeSearch?.id, cursor: pageParam, limit: 50, sort: view.sort, order: view.order, threads: isDrafts ? undefined : "true" }
              : {
                  folder: spanning ? undefined : scopeFolder,
                  folders: spanning ? "all" : undefined,
                  q,
                  label: browse?.kind === "label" ? browse.id : view.labelId,
                  cursor: pageParam,
                  limit: 50,
                  threads: spanning || isDrafts ? undefined : "true",
                  sort: view.sort,
                  order: view.order,
                  from: browse?.kind === "search" ? activeSearch?.from : undefined,
                  to: browse?.kind === "search" ? activeSearch?.to : undefined,
                  since: browse?.kind === "search" ? activeSearch?.since : undefined,
                  until: browse?.kind === "search" ? activeSearch?.until : undefined,
                  ...Object.fromEntries(view.filters.map((f) => [f, "true"])),
                },
          }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
    enabled: !!folders?.isSuccess && (browse?.kind !== "search" || searches.isFetched),
    refetchInterval: refresh,
  })
  const summaries = messages.data?.pages.flatMap((p) => p.messages) ?? []

  const [selected, setSelected] = useState<Selection | null>(null)
  const [showFolders, setShowFolders] = useState(false)
  const [compose, setCompose] = useState<{ key: number; draft: Draft; initial: SenderIdentity } | null>(null)
  const [pendingHolds, setPendingHolds] = useState<UndoHold[]>([])
  const [holdOrder, setHoldOrder] = useState<string[]>([])
  const [seenHoldIds, setSeenHoldIds] = useState("")
  const [helpOpen, setHelpOpen] = useState(false)
  const searchRef = useRef<HTMLInputElement>(null)
  const seenHolds = useRef(new Set<string>())
  const trackedRef = useRef<UndoHold[]>([])
  const router = useRouter()
  const params = useSearchParams()
  const composeTo = params.get("compose")
  const groupTo = params.get("group")
  const linkMailbox = params.get("mailbox")
  const linkFolder = params.get("folder")
  const linkMessage = params.get("message")
  const linkKey = linkMailbox && linkFolder ? `${linkMailbox}/${linkFolder}/${linkMessage ?? ""}` : ""
  const [appliedLink, setAppliedLink] = useState<string | null>(null)
  const [composedTo, setComposedTo] = useState<string | null>(null)
  if (mailbox && composeTo !== composedTo) {
    setComposedTo(composeTo)
    if (composeTo && from) {
      const to = composeTo.split(",").map((address) => address.trim()).filter(Boolean).map((address) => ({ address }))
      setCompose({ key: (compose?.key ?? 0) + 1, initial: from, draft: { ...buildDraft("new", undefined, [mailbox.address], folder), to } })
    }
  }
  const linked = params.get("quarantine")
  const [linkedTo, setLinkedTo] = useState<string | null>(null)
  if (mailboxes.data && linked !== linkedTo) {
    setLinkedTo(linked)
    const target = mailboxes.data.find((m) => m.id === linked)
    if (target) {
      if (target.kind !== "shared") setPrimaryId(target.id)
      setMailboxId(target.id)
      setQuarantineView(true)
      setQuarantineId(params.get("item"))
      setSelected(null)
    }
  }
  if (mailbox && linkKey && appliedLink !== linkKey && linkMailbox && linkFolder && mailboxes.data?.some((m) => m.id === linkMailbox)) {
    setAppliedLink(linkKey)
    setMailboxId(linkMailbox)
    setFolderName(linkFolder)
    setBrowse(null)
    setScheduledView(false)
    setQuarantineView(false)
    const uid = Number(linkMessage)
    if (linkMessage && Number.isInteger(uid) && uid > 0) setSelected({ uid, folder: linkFolder })
  }
  useEffect(() => {
    if ((composeTo && composeTo === composedTo) || (linked && linked === linkedTo) || (linkKey && appliedLink === linkKey && !composeTo)) router.replace("/mail", { scroll: false })
  }, [composeTo, composedTo, linked, linkedTo, linkKey, appliedLink, router])
  useEffect(() => {
    if (!live || !mbx || !folder) return
    if (linkMailbox && linkFolder && (mbx !== linkMailbox || folder !== linkFolder)) return
    const send = () => {
      api("/me/live-focus", { method: "PUT", body: { mailboxId: mbx, folder } }).catch(() => {})
    }
    send()
    const id = window.setInterval(send, 20_000)
    return () => window.clearInterval(id)
  }, [live, mbx, folder, linkMailbox, linkFolder])
  const sender = mailbox?.address
  const composeReady = Boolean(from && sender)
  useEffect(() => {
    if (!groupTo || !composeReady || !sender) return
    const slash = groupTo.indexOf("/")
    if (slash <= 0 || slash === groupTo.length - 1) return
    const book = groupTo.slice(0, slash)
    const id = groupTo.slice(slash + 1)
    let active = true
    api<ContactGroup>(`/contacts/groups/${encodeURIComponent(id)}`, { query: { book } })
      .then((group) => {
        if (!active) return
        const seen = new Set<string>()
        const to = group.members.flatMap((member) =>
          member.emails.filter((address) => {
            const key = address.toLowerCase()
            if (!address || seen.has(key)) return false
            seen.add(key)
            return true
          }).map((address) => (member.name ? { name: member.name, address } : { address }))
        )
        if (to.length === 0) toast.error(`${group.name} has no email addresses`)
        else if (from) setCompose((current) => ({ key: (current?.key ?? 0) + 1, initial: from, draft: { ...buildDraft("new", undefined, [sender], folder), to } }))
        router.replace("/mail", { scroll: false })
      })
      .catch((error) => {
        if (!active) return
        toast.error(errorMessage(error))
        router.replace("/mail", { scroll: false })
      })
    return () => {
      active = false
    }
  }, [groupTo, composeReady, sender, folder, router, from])

  const message = useQuery({
    queryKey: ["message", mbx, selected?.folder, selected?.uid],
    queryFn: () => api<Message>(`/mailboxes/${mbx}/messages/${selected?.uid}`, { query: { folder: selected?.folder } }),
    enabled: selected !== null,
  })

  function located(summary: MessageSummary) {
    return summary.folder || scopeFolder
  }

  function patchCaches(uid: number, messageFolder: string, patch: Partial<MessageSummary> | null) {
    qc.setQueriesData<InfiniteData<MessagePage>>({ queryKey: listKey }, (data) =>
      data && {
        ...data,
        pages: data.pages.map((page) => ({
          ...page,
          messages: patch
            ? page.messages.map((m) => (m.uid === uid && located(m) === messageFolder ? { ...m, ...patch } : m))
            : page.messages.filter((m) => !(m.uid === uid && located(m) === messageFolder)),
        })),
      }
    )
    if (patch) qc.setQueryData<Message>(["message", mbx, messageFolder, uid], (m) => m && { ...m, ...patch })
  }

  const setFlags = useMutation({
    mutationFn: ({ uid, folder: messageFolder, flags }: { uid: number; folder: string; flags: Flags }) =>
      api(`/mailboxes/${mbx}/messages/${uid}`, { method: "PATCH", query: { folder: messageFolder }, body: flags }),
    onMutate: ({ uid, folder: messageFolder, flags }) => patchCaches(uid, messageFolder, flags),
    onError: (_e, { uid, folder: messageFolder, flags }) =>
      patchCaches(uid, messageFolder, Object.fromEntries(Object.entries(flags).map(([k, v]) => [k, !v]))),
    onSettled: (_d, _e, { uid, folder: messageFolder }) => {
      qc.invalidateQueries({ queryKey: ["folders", mbx] })
      qc.invalidateQueries({ queryKey: ["message", mbx, messageFolder, uid] })
    },
  })

  const markedSeen = useRef("")
  useEffect(() => {
    const key = `${mbx}/${selected?.folder}/${selected?.uid}`
    if (selected === null || readOnly || !message.data || message.data.uid !== selected.uid || message.data.seen || markedSeen.current === key) return
    markedSeen.current = key
    setFlags.mutate({ uid: selected.uid, folder: selected.folder, flags: { seen: true } })
  }, [mbx, selected, readOnly, message.data, setFlags])

  const remove = useMutation({
    mutationFn: ({ uid, source, to, target, until, unsnooze }: { uid: number; source: string; to?: Folder; target?: Mailbox; until?: Date; unsnooze?: boolean; notice?: string }) =>
      unsnooze
        ? api(`/mailboxes/${mbx}/snoozed/${uid}`, { method: "DELETE" })
        : until
          ? api(`/mailboxes/${mbx}/messages/${uid}/snooze`, { method: "POST", query: { folder: source }, body: { until: toRFC3339(until) } })
          : to
            ? api(`/mailboxes/${mbx}/messages/${uid}/move`, { method: "POST", query: { folder: source }, body: { to: to.name, mailboxId: target?.id } })
            : api(`/mailboxes/${mbx}/messages/${uid}`, { method: "DELETE", query: { folder: source } }),
    onSuccess: (_d, { uid, source, to, target, until, unsnooze, notice }) => {
      const index = summaries.findIndex((m) => m.uid === uid && located(m) === source)
      const next = summaries[index + 1] ?? summaries[index - 1]
      patchCaches(uid, source, null)
      if (selected?.uid === uid && selected.folder === source) setSelected(next ? { uid: next.uid, folder: located(next) } : null)
      toast.success(
        notice ??
        (unsnooze
          ? `Moved back to ${snoozed.data?.find((s) => s.uid === uid)?.returnFolder ?? "its folder"}`
          : until
            ? `Snoozed until ${formatShortDateTime(until)}`
            : to
              ? `Moved to ${folderLabel(to)}${target ? ` in ${target.address}` : ""}`
              : "Message deleted")
      )
      qc.invalidateQueries({ queryKey: ["folders"] })
      qc.invalidateQueries({ queryKey: ["messages"] })
      qc.invalidateQueries({ queryKey: ["snoozed", mbx] })
    },
  })

  const copy = useMutation({
    mutationFn: ({ uid, source, to, target }: { uid: number; source: string; to: Folder; target?: Mailbox }) =>
      api(`/mailboxes/${mbx}/messages/${uid}/copy`, { method: "POST", query: { folder: source }, body: { to: to.name, mailboxId: target?.id } }),
    onSuccess: (_d, { to, target }) => {
      toast.success(`Copied to ${folderLabel(to)}${target ? ` in ${target.address}` : ""}`)
      qc.invalidateQueries({ queryKey: ["folders", target?.id ?? mbx] })
      qc.invalidateQueries({ queryKey: ["messages", target?.id ?? mbx] })
    },
  })

  async function openDraftOn(mailboxId: string, uid: number, draftFolder: string) {
    const own = identities.filter((i) => i.mailbox.id === mailboxId)
    const fallback = own[0]
    if (!fallback) return
    const saved = await qc.fetchQuery({
      queryKey: ["message", mailboxId, draftFolder, uid],
      queryFn: () => api<Message>(`/mailboxes/${mailboxId}/messages/${uid}`, { query: { folder: draftFolder } }),
    })
    const initial = composeIdentity("draft", saved, own, fallback)
    if (saved.from[0] && initial.from.address !== saved.from[0].address.toLowerCase()) toast.info(`You can no longer send from ${saved.from[0].address}, so this draft now uses ${initial.from.address}.`)
    const draft = buildDraft("draft", saved, [fallback.mailbox.address], draftFolder)
    setCompose((current) => ({ key: (current?.key ?? 0) + 1, draft, initial }))
  }

  async function openDraft(uid: number, draftFolder: string) {
    if (!mailbox || from?.mailbox.id !== mbx) return
    await openDraftOn(mbx, uid, draftFolder)
  }

  const undoSend = useMutation({
    mutationFn: ({ mailboxId, id }: { mailboxId: string; id: string; hold?: boolean }) =>
      api<{ uid: number; folder: string }>(`/mailboxes/${mailboxId}/scheduled/${id}`, { method: "DELETE" }),
    onSuccess: async (draft, { mailboxId, id }) => {
      setPendingHolds((current) => current.filter((item) => item.id !== id))
      qc.invalidateQueries({ queryKey: ["scheduled", mailboxId] })
      qc.invalidateQueries({ queryKey: ["folders", mailboxId] })
      qc.invalidateQueries({ queryKey: ["messages", mailboxId] })
      await openDraftOn(mailboxId, draft.uid, draft.folder)
    },
  })

  function rememberSearch() {
    if (browse?.kind === "search") setSearch(heldSearch)
  }

  function selectSearch(id: string, mailbox: string, found = qc.getQueryData<SavedSearch[]>(["saved-searches", mailbox])?.find((item) => item.id === id)) {
    if (!found) return
    if (browse?.kind !== "search") setHeldSearch(search)
    setMailboxId(mailbox)
    setBrowse({ kind: "search", id })
    setSearch(found.query)
    setSearchView({ sort: view.sort, order: view.order, filters: filtersFromFlags(found), labelId: found.labelId || undefined })
    setScheduledView(false)
    setQuarantineView(false)
    setQuarantineId(null)
    setSelected(null)
    setShowFolders(false)
  }

  function searchInput(name: string) {
    const saved = browse?.kind === "search" ? activeSearch : undefined
    const allFolders = saved ? saved.allFolders : browse?.kind === "label"
    return savedSearchBody({
      name,
      query: search.trim(),
      unread: view.filters.includes("unread"),
      flagged: view.filters.includes("flagged"),
      hasAttachments: view.filters.includes("hasAttachments"),
      from: saved?.from,
      to: saved?.to,
      since: saved?.since,
      until: saved?.until,
      labelId: browse?.kind === "label" ? browse.id : view.labelId,
      folder: allFolders ? "" : saved?.folder || scopeFolder,
      allFolders,
    })
  }

  function storeSearch(saved: SavedSearch) {
    qc.setQueryData<SavedSearch[]>(["saved-searches", mbx], (current) =>
      [...(current ?? []).filter((item) => item.id !== saved.id), saved].sort((a, b) => a.position - b.position || a.name.localeCompare(b.name))
    )
  }

  const createSearch = useMutation({
    meta: { silent: true },
    mutationFn: (name: string) => api<SavedSearch>(`/mailboxes/${mbx}/saved-searches`, { method: "POST", body: searchInput(name) }),
    onSuccess: (saved) => {
      storeSearch(saved)
      setSavingSearch(false)
      setSearchError("")
      toast.success(`Saved ${saved.name}`)
      selectSearch(saved.id, mbx, saved)
    },
    onError: (e) => setSearchError(errorMessage(e)),
  })

  const updateSearch = useMutation({
    meta: { silent: true },
    mutationFn: () => api<SavedSearch>(`/mailboxes/${mbx}/saved-searches/${activeSearch?.id}`, { method: "PATCH", body: searchInput(activeSearch?.name ?? "") }),
    onSuccess: (saved) => {
      storeSearch(saved)
      setSearchView((current) => ({ ...current, filters: filtersFromFlags(saved), labelId: saved.labelId || undefined }))
      setSearch(saved.query)
      toast.success(`Updated ${saved.name}`)
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  const setLabels = useMutation({
    meta: { silent: true },
    mutationFn: ({ uid, folder: messageFolder, label, on }: { uid: number; folder: string; label: Label; on: boolean }) =>
      api(`/mailboxes/${mbx}/messages/${uid}`, {
        method: "PATCH",
        query: { folder: messageFolder },
        body: on ? { addLabels: [label.id] } : { removeLabels: [label.id] },
      }),
    onMutate: ({ uid, folder: messageFolder, label, on }) => {
      const previous = qc.getQueryData<Message>(["message", mbx, messageFolder, uid])?.keywords
      const next = (previous ?? summaries.find((m) => m.uid === uid && located(m) === messageFolder)?.keywords ?? []).filter((keyword) => keyword !== label.keyword)
      if (on) next.push(label.keyword)
      patchCaches(uid, messageFolder, { keywords: next })
      return { previous, uid, folder: messageFolder }
    },
    onError: (e, _vars, ctx) => {
      if (ctx) patchCaches(ctx.uid, ctx.folder, { keywords: ctx.previous })
      toast.error(errorMessage(e))
    },
    onSettled: (_d, _e, { uid, folder: messageFolder }) => {
      qc.invalidateQueries({ queryKey: ["message", mbx, messageFolder, uid] })
      qc.invalidateQueries({ queryKey: ["messages", mbx] })
    },
  })

  async function open(summary: MessageSummary) {
    const messageFolder = located(summary)
    if (folders?.data?.find((f) => f.name === messageFolder)?.specialUse === "drafts" && from?.mailbox.id === mbx) return openDraft(summary.uid, messageFolder)
    setSelected({ uid: summary.uid, folder: messageFolder })
    if (!summary.seen && !readOnly) for (const uid of summary.threadUids ?? [summary.uid]) setFlags.mutate({ uid, folder: messageFolder, flags: { seen: true } })
  }


  function step(delta: number) {
    if (!summaries.length) return
    const index = summaries.findIndex((m) => m.uid === selected?.uid && located(m) === selected?.folder)
    const target = summaries[index === -1 ? 0 : Math.min(Math.max(index + delta, 0), summaries.length - 1)]
    if (target) open(target)
  }

  function startCompose(mode: ComposeMode) {
    if (!mailbox || !from) return
    if (mode !== "new" && !message.data) return
    const initial = composeIdentity(mode, message.data, identities, from)
    const draft = buildDraft(mode, message.data, [mailbox.address, initial.from.address], selected?.folder ?? folder)
    if (initial.mailbox.id !== mbx && draft.forwardFrom) {
      toast.info(`Forwarded attachments can't be sent from ${initial.from.address}, so they were left out.`)
      delete draft.forwardFrom
    }
    setCompose((current) => ({ key: (current?.key ?? 0) + 1, draft, initial }))
  }

  function selectFolder(id: string, name: string) {
    rememberSearch()
    setBrowse(null)
    setMailboxId(id)
    setFolderName(name)
    setScheduledView(false)
    setQuarantineView(false)
    setQuarantineId(null)
    setSelected(null)
    setShowFolders(false)
  }

  function selectMailbox(id: string) {
    rememberSearch()
    setBrowse(null)
    setMailboxId(id)
    setScheduledView(false)
    setQuarantineView(false)
    setQuarantineId(null)
    setFolderName(null)
    setSelected(null)
  }

  function selectLabel(id: string, mailbox: string) {
    rememberSearch()
    setMailboxId(mailbox)
    setBrowse({ kind: "label", id })
    setScheduledView(false)
    setQuarantineView(false)
    setQuarantineId(null)
    setSelected(null)
    setShowFolders(false)
  }

  const ticking =
    pendingHolds.length > 0 ||
    (scheduled.data ?? []).some(isUndoHold) ||
    extraScheduled.some((query) => (query.data ?? []).some(isUndoHold))
  const now = useNow(ticking)
  const queryHolds: UndoHold[] = [
    ...(scheduled.data ?? []).filter(isUndoHold).map((message) => ({ ...message, mailboxId: mbx })),
    ...extraScheduled.flatMap((query, index) => (query.data ?? []).filter(isUndoHold).map((message) => ({ ...message, mailboxId: extraIds[index] }))),
  ]
  const queryHoldIds = new Set(queryHolds.map((item) => item.id))
  const arrived = [...queryHolds, ...pendingHolds.filter((item) => !queryHoldIds.has(item.id) && Date.parse(item.sendAt) > now)]
  const arrivedIds = arrived.map((item) => item.id).join("\n")
  if (arrivedIds !== seenHoldIds) {
    setSeenHoldIds(arrivedIds)
    setHoldOrder((current) => {
      const ids = arrivedIds ? arrivedIds.split("\n") : []
      const present = new Set(ids)
      const next = current.filter((id) => present.has(id))
      for (const id of ids) if (!next.includes(id)) next.push(id)
      return next
    })
  }
  const newestId = [...holdOrder].reverse().find((id) => arrived.some((item) => item.id === id))
  const holds = [...arrived].sort((a, b) => a.sendAt.localeCompare(b.sendAt))
  const dueKey = holds.map((item) => `${item.id}@${item.sendAt}`).join("|")
  const tracked = [
    ...(scheduled.data ?? []).map((message) => ({ ...message, mailboxId: mbx })),
    ...extraScheduled.flatMap((query, index) => (query.data ?? []).map((message) => ({ ...message, mailboxId: extraIds[index] }))),
  ]
  const trackedKey = tracked.map((message) => `${message.id}:${message.undo ? "1" : "0"}:${message.error ?? ""}`).join("|")

  useEffect(() => {
    trackedRef.current = tracked
  })
  useEffect(() => {
    for (const message of trackedRef.current) {
      if (isUndoHold(message)) seenHolds.current.add(message.id)
      else if (message.undo && message.error && seenHolds.current.delete(message.id)) toast.error(message.error)
    }
  }, [trackedKey])

  useEffect(() => {
    if (!dueKey) return
    const due = Math.min(...dueKey.split("|").map((part) => Date.parse(part.slice(part.indexOf("@") + 1))))
    const timer = window.setTimeout(() => qc.invalidateQueries({ queryKey: ["scheduled"] }), Math.max(0, due - now) + 400)
    return () => window.clearTimeout(timer)
  }, [dueKey, now, qc])

  const shortcuts = useRef<(e: KeyboardEvent) => void>(() => {})
  useEffect(() => {
    shortcuts.current = (e: KeyboardEvent) => {
      const newest = holds.find((item) => item.id === newestId)
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "z") {
        if (!newest || compose || helpOpen || isTyping(e.target) || undoSend.isPending) return
        e.preventDefault()
        undoSend.mutate({ mailboxId: newest.mailboxId, id: newest.id, hold: true })
        return
      }
      if (e.metaKey || e.ctrlKey || e.altKey || compose || helpOpen || isTyping(e.target)) return
      if (quarantineView && !["c", "?", "/"].includes(e.key)) {
        if (e.key === "Escape") setQuarantineId(null)
        return
      }
      const uid = selected?.uid ?? null
      const source = selected?.folder ?? ""
      const actions: Record<string, () => void> = {
        c: () => startCompose("new"),
        j: () => step(1),
        k: () => step(-1),
        r: () => startCompose("reply"),
        a: () => startCompose("replyAll"),
        f: () => startCompose("forward"),
        e: () => uid !== null && remove.mutate({ uid, source }),
        "#": () => uid !== null && remove.mutate({ uid, source }),
        "/": () => searchRef.current?.focus(),
        "?": () => setHelpOpen(true),
        Escape: () => setSelected(null),
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

  const reading = selected !== null
  const listFolder = folders?.data?.find((f) => f.name === scopeFolder) ?? { name: scopeFolder, specialUse: "" as const }
  const saveSearch = readOnly
    ? undefined
    : browse?.kind === "search" && activeSearch && searchDirty
      ? { label: "Update search", onClick: () => updateSearch.mutate() }
      : browse?.kind !== "search" && (search.trim() || view.filters.length > 0 || view.labelId || browse?.kind === "label")
        ? { label: "Save search", onClick: () => { setSearchError(""); setSavingSearch(true) } }
        : undefined
  const searchLabel = browse?.kind === "label" ? activeLabel?.name : labels.data?.find((item) => item.id === view.labelId)?.name
  const inspecting = quarantineView && quarantineId !== null

  function afterQuarantine(id: string) {
    const index = held.findIndex((i) => i.id === id)
    setQuarantineId((held[index + 1] ?? held[index - 1])?.id ?? null)
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <StorageBanner key={mbx} mailbox={mailbox} folders={folders?.data} />
      <div className="flex min-h-0 flex-1">
        <FolderPane
          className={cn("w-full shrink-0 md:w-66 lg:flex", showFolders && !reading && !inspecting ? "flex" : "hidden")}
          own={own}
          primary={trees.find((t) => t.mailbox.id === primary?.id)}
          onPrimaryChange={(id) => {
            setPrimaryId(id)
            selectMailbox(id)
          }}
          shared={trees.filter((t) => t.mailbox.id !== primary?.id)}
          active={{ mailboxId: mbx, folder }}
          onFolderChange={selectFolder}
          scheduled={scheduledItems.length}
          scheduledActive={showScheduled}
          onScheduled={() => {
            rememberSearch()
            setBrowse(null)
            setScheduledView(true)
            setQuarantineView(false)
            setQuarantineId(null)
            setSelected(null)
            setShowFolders(false)
          }}
          quarantine={quarantine.data?.pages[0]?.total}
          quarantineActive={quarantineView}
          onQuarantine={() => {
            rememberSearch()
            setBrowse(null)
            setQuarantineView(true)
            setScheduledView(false)
            setSelected(null)
            setShowFolders(false)
          }}
          onCompose={from ? () => startCompose("new") : undefined}
          browse={mailbox.id === mbx ? browse : null}
          onBrowseLabel={(id, labelId) => selectLabel(labelId, id)}
          onBrowseSearch={(id, searchId) => selectSearch(searchId, id)}
          onDropMessage={readOnly ? undefined : (uid, to, target, sourceFolder) => remove.mutate({ uid, source: sourceFolder || scopeFolder, to, target: target === mbx ? undefined : mailboxes.data.find((m) => m.id === target) })}
        />
        {quarantineView ? (
          <QuarantineList
            className={cn("w-full shrink-0 md:flex md:w-[360px] xl:w-[400px]", showFolders || inspecting ? "hidden" : "flex")}
            mailboxId={mbx}
            query={quarantine}
            items={held}
            selectedId={quarantineId}
            readOnly={readOnly}
            onOpen={setQuarantineId}
            onShowFolders={() => setShowFolders(true)}
          />
        ) : showScheduled ? (
          <ScheduledList
            className={cn("w-full shrink-0 md:flex md:w-[360px] xl:w-[400px]", showFolders ? "hidden" : "flex")}
            messages={scheduledItems}
            cancelling={undoSend.isPending && !undoSend.variables?.hold ? undoSend.variables?.id : undefined}
            onCancel={(id) => undoSend.mutate({ mailboxId: mbx, id })}
            onShowFolders={() => setShowFolders(true)}
          />
        ) : (
          <MessageList
            className={cn("w-full shrink-0 md:flex md:w-[360px] xl:w-[400px]", showFolders || reading ? "hidden" : "flex")}
            folder={listFolder}
            notice={spanning ? undefined : retentionNotice(retention.data, listFolder)}
            search={search}
            onSearchChange={setSearch}
            searchRef={searchRef}
            q={q}
            query={messages}
            summaries={browse?.kind === "search" && searches.isSuccess && !activeSearch ? [] : summaries}
            selected={selected}
            onOpen={open}
            onShowFolders={() => setShowFolders(true)}
            onCompose={from ? () => startCompose("new") : undefined}
            onHelp={() => setHelpOpen(true)}
            wakeTimes={!spanning && scopeFolder === snoozedFolder ? wakeTimes : undefined}
            view={view}
            onViewChange={(next) => (browse?.kind === "search" ? setSearchView(next) : browse?.kind === "label" ? setLabelView(next) : setFolderView(next))}
            draggable={!readOnly}
            live={live}
            labels={labels.data}
            labelFilter={browse?.kind !== "label"}
            heading={activeLabel?.name ?? activeSearch?.name}
            swatch={activeLabel?.color}
            searchPlaceholder={spanning ? "Search every folder" : "Search this folder"}
            showFolder={spanning}
            folderTitle={(name) => folderLabel(folders?.data?.find((f) => f.name === name) ?? { name, specialUse: "" })}
            saveSearch={saveSearch}
            emptyTitle={activeLabel ? `No messages labeled ${activeLabel.name}` : activeSearch ? `${activeSearch.name} is empty` : browse?.kind === "search" ? "That saved search is no longer available" : undefined}
            emptyDescription={activeLabel && !q && view.filters.length === 0 ? "Messages with this label appear here, from every folder." : activeSearch && !q && !searchDirty ? "Nothing matches this search right now." : undefined}
          />
        )}
        {quarantineView ? (
          <QuarantinePane
            className={cn("min-w-0 flex-1 md:flex", inspecting ? "flex" : "hidden")}
            mailboxId={mbx}
            itemId={quarantineId}
            readOnly={readOnly}
            onBack={() => setQuarantineId(null)}
            onDone={afterQuarantine}
          />
        ) : (
          <ReadingPane
            className={cn("min-w-0 flex-1 md:flex", reading ? "flex" : "hidden")}
            mailboxId={mbx}
            folder={selected?.folder ?? scopeFolder}
            mailboxes={mailboxes.data.filter((m) => m.id !== mbx && m.access !== "read")}
            query={message}
            selected={selected !== null}
            earlier={summaries.find((m) => m.uid === selected?.uid && located(m) === selected?.folder)?.threadUids?.slice(1) ?? []}
            onBack={() => setSelected(null)}
            onReply={from ? startCompose : undefined}
            onFlags={(flags) => selected && setFlags.mutate({ uid: selected.uid, folder: selected.folder, flags })}
            onMove={(to, target) => selected && remove.mutate({ uid: selected.uid, source: selected.folder, to, target })}
            onCopy={(to, target) => selected && copy.mutate({ uid: selected.uid, source: selected.folder, to, target })}
            onDelete={() => selected && remove.mutate({ uid: selected.uid, source: selected.folder })}
            onSpam={
              spamTarget
                ? () => selected && remove.mutate({ uid: selected.uid, source: selected.folder, to: spamTarget, notice: isJunk ? "Marked as not spam and moved to Inbox" : "Reported as spam and moved to Junk" })
                : undefined
            }
            junk={isJunk}
            onSnooze={(until) => selected && remove.mutate({ uid: selected.uid, source: selected.folder, until })}
            onUnsnooze={selected?.folder === snoozedFolder ? () => selected && remove.mutate({ uid: selected.uid, source: selected.folder, unsnooze: true }) : undefined}
            deleting={remove.isPending}
            readOnly={readOnly}
            onLabels={readOnly ? undefined : (label, on) => selected && setLabels.mutate({ uid: selected.uid, folder: selected.folder, label, on })}
            placedIn={spanning && selected ? folderLabel(folders?.data?.find((f) => f.name === selected.folder) ?? { name: selected.folder, specialUse: "" }) : undefined}
          />
        )}
        {compose && (
          <Composer
            key={compose.key}
            identities={identities}
            initial={compose.initial}
            draft={compose.draft}
            onClose={() => setCompose(null)}
            onHeld={(mailboxId, message) => setPendingHolds((current) => [...current.filter((item) => item.id !== message.id), { ...message, mailboxId }])}
          />
        )}
        <ShortcutsDialog open={helpOpen} onOpenChange={setHelpOpen} />
        {savingSearch && (
          <SearchDialog
            open
            onOpenChange={setSavingSearch}
            title="Save search"
            description={describeSearch({ query: search.trim(), filters: view.filters, label: searchLabel, folder: folderLabel(listFolder), allFolders: spanning })}
            submitLabel="Save search"
            pending={createSearch.isPending}
            error={searchError}
            onSubmit={(name) => createSearch.mutate(name)}
          />
        )}
      </div>
      <UndoSendBar items={holds} undoing={undoSend.isPending ? undoSend.variables?.id : undefined} onUndo={(item) => undoSend.mutate({ mailboxId: item.mailboxId, id: item.id, hold: true })} />
    </div>
  )
}

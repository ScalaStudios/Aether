"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { EditorContent, useEditor } from "@tiptap/react"
import { ChevronDown, Link2, Paperclip, Send, X } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { CopyField } from "@/components/copy-button"
import { Field } from "@/components/field"
import { IconButton } from "@/components/icon-button"
import { FormatToolbar, mailEditorExtensions } from "@/components/mail/format-toolbar"
import { LaterMenu } from "@/components/mail/later-menu"
import { ProtectionControls, type ProtectionMode, type ProtectionProtocol } from "@/components/mail/protection-controls"
import { RecipientInput } from "@/components/recipient-input"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useSession } from "@/hooks/use-session"
import { api, errorMessage } from "@/lib/api"
import { bytesToBase64, readableSecret } from "@/lib/crypto/bytes"
import { autocryptHeader } from "@/lib/crypto/openpgp"
import { sealProtected } from "@/lib/crypto/protected"
import { keyFor } from "@/lib/crypto/session"
import { sealOutgoing } from "@/lib/crypto/seal"
import { signaturesQuery } from "@/lib/queries"
import { toRFC3339 } from "@/lib/calendar"
import { formatAddress, formatBytes, formatListDate, formatShortDateTime } from "@/lib/format"
import { useI18n } from "@/lib/i18n"
import type { Draft, SenderIdentity } from "@/lib/mail"
import type { Attachment, EncryptionKey, EncryptionPolicy, FileLinkSummary, OutgoingMessage, RecipientKey, SavedDraft, ScheduledMessage, TransferLimits } from "@/lib/types"

const address = z.object({ name: z.string().optional(), address: z.email() })

type Values = {
  to: z.infer<typeof address>[]
  cc: z.infer<typeof address>[]
  bcc: z.infer<typeof address>[]
  subject: string
}

const fallbackLimits: TransferLimits = {
  messageBytes: 50 * 1024 * 1024,
  attachmentBytes: 20 * 1024 * 1024,
  linkThresholdBytes: 20 * 1024 * 1024,
  linkFileBytes: 2 * 1024 * 1024 * 1024,
  storageBytes: 10 * 1024 * 1024 * 1024,
  usedBytes: 0,
  maxInlineFiles: 20,
  maxFileLinks: 20,
  platform: { messageBytes: 50 * 1024 * 1024, linkFileBytes: 2 * 1024 * 1024 * 1024, storageBytes: 100 * 1024 * 1024 * 1024 },
}

interface ComposerLink {  localId: string
  file?: File
  id?: string
  filename: string
  size: number
  contentType: string
  expires: string
  passwordProtected: boolean
  password: string
  clearPassword: boolean
  uploading: boolean
  error?: string
}

function readBase64(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result).slice(String(reader.result).indexOf(",") + 1))
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

function toLocalInput(iso: string | null) {
  if (!iso) return ""
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ""
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function linkFromSummary(link: FileLinkSummary): ComposerLink {
  return {
    localId: link.id,
    id: link.id,
    filename: link.filename,
    size: link.sizeBytes,
    contentType: link.contentType,
    expires: toLocalInput(link.expiresAt),
    passwordProtected: link.passwordProtected,
    password: "",
    clearPassword: false,
    uploading: false,
  }
}

function linkRefs(items: ComposerLink[], includePassword: boolean): NonNullable<OutgoingMessage["fileLinks"]> {
  return items.flatMap((link) => {
    if (!link.id) return []
    const expires = link.expires ? new Date(link.expires) : null
    return [{
      id: link.id,
      expiresAt: expires && !Number.isNaN(expires.getTime()) ? toRFC3339(expires) : undefined,
      clearExpiry: link.expires ? undefined : true,
      ...(includePassword ? { password: link.password || undefined, clearPassword: link.clearPassword || undefined } : {}),
    }]
  })
}

function AttachmentChip({ name, size, onRemove, action }: { name: string; size: number; onRemove: () => void; action?: React.ReactNode }) {
  const { t } = useI18n()
  return (
    <li className="flex min-h-10 max-w-full items-center gap-2 rounded-lg border border-strong py-1 pr-1 pl-3 text-sm sm:max-w-[360px]">
      <Paperclip aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
      <span className="truncate">{name}</span>
      <span className="shrink-0 text-xs text-fg-tertiary">{formatBytes(size)}</span>
      {action}
      <IconButton type="button" label={t("common.removeItem", { item: name })} size="icon-sm" onClick={onRemove}>
        <X />
      </IconButton>
    </li>
  )
}

function flyAway(icon: SVGElement | null) {
  if (!icon || matchMedia("(prefers-reduced-motion: reduce)").matches) return
  const r = icon.getBoundingClientRect()
  const plane = icon.cloneNode(true) as SVGElement
  plane.setAttribute("aria-hidden", "true")
  Object.assign(plane.style, { position: "fixed", left: `${r.left}px`, top: `${r.top}px`, width: `${r.width}px`, height: `${r.height}px`, zIndex: "100", pointerEvents: "none", color: "var(--color-ember)" })
  document.body.append(plane)
  plane
    .animate(
      [
        { transform: "translate(0, 0) rotate(0deg) scale(1)", opacity: 1 },
        { transform: "translate(-6px, 4px) rotate(-8deg) scale(1.15)", opacity: 1, offset: 0.2 },
        { transform: "translate(120px, -90px) rotate(-14deg) scale(1.3)", opacity: 1, offset: 0.6 },
        { transform: "translate(260px, -260px) rotate(-20deg) scale(0.8)", opacity: 0 },
      ],
      { duration: 800, easing: "cubic-bezier(0.45, 0, 0.55, 1)" }
    )
    .finished.finally(() => plane.remove())
}

export function Composer({
  identities,
  initial,
  draft,
  onClose,
  onHeld,
}: {
  identities: SenderIdentity[]
  initial: SenderIdentity
  draft: Draft
  onClose: () => void
  onHeld?: (mailboxId: string, message: ScheduledMessage) => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const undoSeconds = useSession().data?.user.undoSendSeconds ?? 0
  const [identity, setIdentity] = useState(initial)
  const { mailbox, from } = identity
  const [showCopies, setShowCopies] = useState(draft.cc.length > 0)
  const [files, setFiles] = useState<File[]>([])
  const [links, setLinks] = useState<ComposerLink[]>(() => (draft.fileLinks ?? []).map(linkFromSummary))
  const [forwarded, setForwarded] = useState<Attachment[]>(draft.forwardFrom?.attachments ?? [])
  const [source, setSource] = useState(draft.forwardFrom ? { folder: draft.forwardFrom.folder, uid: draft.forwardFrom.uid } : null)
  const [draftUid, setDraftUid] = useState(draft.draftUid)
  const [savedAt, setSavedAt] = useState<Date | null>(null)
  const [changes, setChanges] = useState(0)
  const dirty = useRef(false)
  const pendingSave = useRef<Promise<unknown> | null>(null)
  const [dragging, setDragging] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const sendIcon = useRef<SVGSVGElement>(null)
  const [guard, setGuard] = useState<ProtectionMode>("standard")
  const guardRef = useRef<ProtectionMode>("standard")
  const [protocol, setProtocol] = useState<ProtectionProtocol>("openpgp")
  const [secret, setSecret] = useState(readableSecret)
  const [hours, setHours] = useState<24 | 72 | 168>(72)
  const [autocrypt, setAutocrypt] = useState(true)
  const [revealed, setRevealed] = useState("")
  const policy = useQuery({ queryKey: ["encryption", mailbox.orgId], queryFn: () => api<EncryptionPolicy>(`/organizations/${mailbox.orgId}/encryption`) })
  const ownKeys = useQuery({ queryKey: ["encryption-keys"], queryFn: () => api<EncryptionKey[]>("/me/encryption-keys") })
  const limitsQuery = useQuery({ queryKey: ["transfer-limits", mailbox.id], queryFn: () => api<TransferLimits>(`/mailboxes/${mailbox.id}/transfer-limits`) })
  const limits = limitsQuery.data ?? fallbackLimits
  const inlineBytes = files.reduce((n, f) => n + f.size, 0) + forwarded.reduce((n, a) => n + a.size, 0)

  const schema = useMemo(
    () =>
      z.object({
        to: z.array(address).min(1, t("mail.composer.addRecipient")),
        cc: z.array(address),
        bcc: z.array(address),
        subject: z.string(),
      }),
    [t]
  )

  const touch = useCallback(() => {
    dirty.current = true
    setChanges((n) => n + 1)
  }, [])

  function uploadLink(localId: string, file: File) {
    setLinks((current) => current.map((link) => (link.localId === localId ? { ...link, uploading: true, error: undefined } : link)))
    api<FileLinkSummary>(`/mailboxes/${mailbox.id}/file-links`, { method: "POST", query: { filename: file.name }, body: file })
      .then((saved) => {
        setLinks((current) => current.map((link) => (link.localId === localId ? { ...linkFromSummary(saved), localId, file, password: link.password, expires: link.expires, clearPassword: link.clearPassword } : link)))
        touch()
      })
      .catch((error) => {
        const message = errorMessage(error)
        toast.error(message)
        setLinks((current) => current.map((link) => (link.localId === localId ? { ...link, uploading: false, error: message } : link)))
      })
  }

  function removeLink(link: ComposerLink) {
    setLinks((current) => current.filter((item) => item.localId !== link.localId))
    if (link.id) {
      api(`/mailboxes/${mailbox.id}/file-links/${link.id}`, { method: "DELETE" }).catch((error) => toast.error(errorMessage(error)))
    }
    touch()
  }

  function sendFileAsLink(file: File, index: number) {
    if (links.length >= limits.maxFileLinks) {
      toast.error(`Send ${limits.maxFileLinks} download links or fewer.`)
      return
    }
    if (file.size > limits.linkFileBytes) {
      toast.error(`${file.name} is larger than ${formatBytes(limits.linkFileBytes)}, the limit for a download link.`)
      return
    }
    const localId = crypto.randomUUID()
    setFiles((current) => current.filter((_, i) => i !== index))
    setLinks((current) => [...current, { localId, file, filename: file.name, size: file.size, contentType: file.type || "application/octet-stream", expires: "", passwordProtected: false, password: "", clearPassword: false, uploading: true }])
    uploadLink(localId, file)
    touch()
  }

  function addFiles(list: FileList | null) {
    if (!list?.length) return
    const rejected: string[] = []
    const inline = [...files]
    const linked = [...links]
    let bytes = inline.reduce((n, file) => n + file.size, 0) + forwarded.reduce((n, file) => n + file.size, 0)
    const uploads: { localId: string; file: File }[] = []
    for (const file of Array.from(list)) {
      if (file.size > limits.linkFileBytes) {
        rejected.push(`${file.name} is larger than ${formatBytes(limits.linkFileBytes)}, the limit for a download link.`)
        continue
      }
      const asLink = file.size > limits.linkThresholdBytes || inline.length + forwarded.length >= limits.maxInlineFiles || bytes + file.size > limits.attachmentBytes
      if (asLink) {
        if (linked.length >= limits.maxFileLinks) {
          rejected.push(`Send ${limits.maxFileLinks} download links or fewer.`)
          continue
        }
        const localId = crypto.randomUUID()
        linked.push({ localId, file, filename: file.name, size: file.size, contentType: file.type || "application/octet-stream", expires: "", passwordProtected: false, password: "", clearPassword: false, uploading: true })
        uploads.push({ localId, file })
        continue
      }
      inline.push(file)
      bytes += file.size
    }
    if (rejected.length) toast.error(rejected[0])
    setFiles(inline)
    setLinks(linked)
    for (const item of uploads) uploadLink(item.localId, item.file)
    if (uploads.length || inline.length !== files.length) touch()
  }
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { to: draft.to, cc: draft.cc, bcc: draft.bcc, subject: draft.subject },
  })
  const placingSignature = useRef(false)
  const signatures = useQuery(signaturesQuery(mailbox.id, from.address))
  const editor = useEditor({
    extensions: mailEditorExtensions,
    content: draft.html,
    immediatelyRender: false,
    onUpdate: () => {
      if (!placingSignature.current) touch()
    },
    autofocus: draft.to.length ? "start" : false,
    editorProps: {
      attributes: {
        class: "prose-editor min-h-[240px] px-4 py-3 text-base outline-none",
        role: "textbox",
        "aria-multiline": "true",
        "aria-label": t("mail.composer.messageBody"),
        "data-focus-ring": "off",
      },
    },
  })

  async function payload(values: Values, uploads: File[], includePassword: boolean): Promise<OutgoingMessage> {
    const attachments = await Promise.all(uploads.map(async (f) => ({ filename: f.name, contentType: f.type || "application/octet-stream", data: await readBase64(f) })))
    const refs = linkRefs(links, includePassword)
    return {
      ...values,
      fromAddress: from.address,
      html: editor?.getHTML() ?? "",
      text: editor?.getText({ blockSeparator: "\n\n" }) ?? "",
      inReplyTo: draft.inReplyTo,
      references: draft.references?.length ? draft.references : undefined,
      attachments: attachments.length ? attachments : undefined,
      fileLinks: refs.length ? refs : undefined,
      forwardFrom: source && forwarded.length ? { ...source, parts: forwarded.map((a) => a.part) } : undefined,
      draftUid,
    }
  }

  const saveDraft = useMutation({
    mutationFn: async (uploads: File[]) => api<SavedDraft>(`/mailboxes/${mailbox.id}/drafts`, { method: "PUT", body: await payload(form.getValues(), uploads, false) }),
    meta: { silent: true },
    onSuccess: (saved, uploads) => {
      setDraftUid(saved.uid)
      setSource({ folder: saved.folder, uid: saved.uid })
      setForwarded(saved.attachments)
      setFiles((current) => current.filter((f) => !uploads.includes(f)))
      setLinks((current) => {
        const pending = current.filter((link) => !link.id)
        const kept = saved.fileLinks.map((link) => {
          const previous = current.find((item) => item.id === link.id)
          return previous ? { ...linkFromSummary(link), localId: previous.localId, file: previous.file, password: previous.password, expires: previous.expires, clearPassword: previous.clearPassword } : linkFromSummary(link)
        })
        return [...kept, ...pending]
      })
      setSavedAt(new Date())
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
    },
    onError: () => {
      dirty.current = true
    },
  })

  const signaturePlaced = useRef(draft.mode === "draft" ? initial.from.address : "")
  useEffect(() => {
    const settings = signatures.data
    if (!editor || !settings || signaturePlaced.current === from.address) return
    signaturePlaced.current = from.address
    const newMessage = draft.mode === "new" || (draft.mode === "draft" && !draft.inReplyTo)
    const chosen = settings.signatures.find((sig) => sig.id === (newMessage ? settings.newMessage : settings.replies))
    const html = chosen && `<div data-aether-signature="true">${chosen.html}</div>`
    const doc = editor.state.doc
    let start = -1
    let end = -1
    doc.forEach((node, offset) => {
      if (node.type.name === "automaticSignature") {
        start = offset
        end = offset + node.nodeSize
      }
    })
    placingSignature.current = true
    if (start >= 0 && html) editor.commands.insertContentAt({ from: start, to: end }, html, { updateSelection: false })
    else if (start >= 0) editor.commands.deleteRange({ from: start, to: end })
    else if (html) editor.commands.insertContentAt(newMessage ? doc.content.size : (doc.firstChild?.nodeSize ?? 0), `<p></p>${html}${newMessage ? "" : "<p></p>"}`, { updateSelection: false })
    placingSignature.current = false
  }, [editor, signatures.data, draft.mode, draft.inReplyTo, from.address])

  const readOnly = mailbox.access === "read"
  const save = useCallback(() => {
    if (guardRef.current !== "standard" || readOnly || pendingSave.current || !dirty.current) return pendingSave.current
    dirty.current = false
    pendingSave.current = saveDraft.mutateAsync(files).catch(() => undefined).finally(() => {
      pendingSave.current = null
    })
    return pendingSave.current
  }, [files, saveDraft, readOnly])

  useEffect(() => form.subscribe({ formState: { values: true }, callback: touch }), [form, touch])

  useEffect(() => {
    if (!changes) return
    const timer = setTimeout(save, 2500)
    return () => clearTimeout(timer)
  }, [changes, save])

  const activeProtocol: ProtectionProtocol = policy.data?.openpgp === "off" && policy.data.smime !== "off" ? "smime" : policy.data?.smime === "off" ? "openpgp" : protocol

  async function protectedBody(values: Values, plain: OutgoingMessage, sendAt?: Date) {
    if (guard === "password") {
      if (values.cc.length || values.bcc.length || values.to.length !== 1) throw new Error("A password-protected message goes to one person, with no Cc or Bcc.")
      if (files.length || forwarded.length) throw new Error("A password-protected message can't include attachments.")
      if (secret.trim().length < 8) throw new Error("Choose a password of at least 8 characters.")
      const id = crypto.randomUUID()
      const sealed = await sealProtected(secret, id, { subject: values.subject, text: plain.text ?? "", html: plain.html ?? "" })
      await api(`/mailboxes/${mailbox.id}/protected`, {
        method: "POST",
        body: { id, to: values.to[0], access: sealed.access, replyHash: sealed.replyHash, salt: sealed.salt, message: sealed.message, hours },
      })
      return id
    }
    const record = ownKeys.data?.find((key) => key.protocol === activeProtocol && key.address.toLowerCase() === mailbox.address.toLowerCase())
    const mine = keyFor(mailbox.address, activeProtocol)
    if (!record || !mine) throw new Error("Unlock the key for this address under Settings → Encryption.")
    let recipientKeys: string[] = []
    if (guard === "encrypt") {
      const people = [...values.to, ...values.cc, ...values.bcc]
      recipientKeys = await Promise.all(people.map(async (person) => {
        const found = await api<RecipientKey[]>("/me/recipient-keys", { query: { address: person.address } })
        const match = found.find((key) => key.protocol === activeProtocol)
        if (match) return match.publicKey
        if (activeProtocol === "openpgp") {
          try {
            return (await api<RecipientKey>("/me/wkd", { query: { address: person.address } })).publicKey
          } catch {
            throw new Error(`No OpenPGP key was found for ${person.address}.`)
          }
        }
        throw new Error(`No S/MIME certificate was found for ${person.address}.`)
      }))
    }
    const carried = source ? await Promise.all(forwarded.map(async (file) => {
      const blob = await api<Blob>(`/mailboxes/${mailbox.id}/messages/${source.uid}/attachments/${file.part}`, { query: { folder: source.folder }, blob: true })
      return { filename: file.filename, contentType: file.contentType, data: bytesToBase64(new Uint8Array(await blob.arrayBuffer())) }
    })) : []
    const sealed = await sealOutgoing({
      text: plain.text ?? "",
      html: plain.html ?? "",
      attachments: [...(plain.attachments ?? []), ...carried],
      protocol: activeProtocol,
      encrypt: guard === "encrypt",
      privateKey: mine.privateKey,
      certificate: record.publicKey,
      recipientKeys,
    })
    const header = activeProtocol === "openpgp" && autocrypt ? await autocryptHeader(mailbox.address, mine.privateKey) : undefined
    await api(`/mailboxes/${mailbox.id}/send`, {
      method: "POST",
      body: { to: values.to, cc: values.cc, bcc: values.bcc, subject: values.subject, inReplyTo: plain.inReplyTo, references: plain.references, sealed, autocrypt: header, sendAt: sendAt && toRFC3339(sendAt) },
    })
    return ""
  }

  const send = useMutation({
    mutationFn: async ({ values, sendAt }: { values: Values; sendAt?: Date }) => {
      await pendingSave.current
      const plain = await payload(values, files, true)
      if (guard === "standard") {
        return api<ScheduledMessage | { messageId: string }>(`/mailboxes/${mailbox.id}/send`, { method: "POST", body: { ...plain, sendAt: sendAt && toRFC3339(sendAt) } })
      }
      const id = await protectedBody(values, plain, sendAt)
      if (id) setRevealed(secret)
    },
    onSuccess: (result, { sendAt }) => {
      qc.invalidateQueries({ queryKey: ["scheduled", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      if (guard === "password") return
      if (guard !== "standard") {
        toast.success(sendAt ? `Scheduled for ${formatShortDateTime(sendAt)}` : "Protected message sent")
        onClose()
        return
      }
      if (sendAt) {
        toast.success(`Scheduled for ${formatShortDateTime(sendAt)}`)
      } else if (result && "undo" in result && result.undo) {
        onHeld?.(mailbox.id, result)
        qc.setQueryData<ScheduledMessage[]>(["scheduled", mailbox.id], (current) => [result, ...(current ?? []).filter((item) => item.id !== result.id)])
      } else {
        flyAway(sendIcon.current)
        toast.success("Message sent")
      }
      onClose()
    },
  })

  const discard = useMutation({
    mutationFn: async () => {
      await pendingSave.current
      if (draftUid && !readOnly) await api(`/mailboxes/${mailbox.id}/drafts/${draftUid}`, { method: "DELETE" })
    },
    onSuccess: () => {
      if (draftUid && !readOnly) toast.success(t("mail.composer.toasts.discarded"))
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
      onClose()
    },
  })

  async function chooseProtection(next: ProtectionMode) {
    guardRef.current = next
    setGuard(next)
    if (next === "password") {
      form.setValue("cc", [])
      form.setValue("bcc", [])
      setShowCopies(false)
      if (files.length || forwarded.length) {
        setFiles([])
        setForwarded([])
        toast.info("Attachments were removed. A password-protected message can't include them.")
      }
    }
    if (next === "standard") return
    dirty.current = false
    await pendingSave.current
    if (!draftUid || readOnly) return
    const uid = draftUid
    setDraftUid(undefined)
    await api(`/mailboxes/${mailbox.id}/drafts/${uid}`, { method: "DELETE" }).catch(() => undefined)
    qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
  }

  async function close() {
    if (revealed) return
    if (guardRef.current !== "standard") {
      onClose()
      return
    }
    dirty.current = dirty.current || files.length > 0 || links.some((link) => link.id)
    await save()
    if (!readOnly && (draftUid || savedAt || saveDraft.data)) toast.success(t("mail.composer.toasts.savedToDrafts"))
    onClose()
  }

  function changeFrom(id: string) {
    const next = identities.find((i) => i.from.address === id)
    if (!next) return
    setIdentity(next)
    touch()
    if (next.mailbox.id === mailbox.id) return
    if (forwarded.length) toast.info(`Forwarded attachments can't be sent from ${next.from.address}, so they were left out.`)
    if (links.length) toast.info(`Download links can't be sent from ${next.from.address}, so they were left out.`)
    setDraftUid(undefined)
    setSource(null)
    setForwarded([])
    setLinks([])
  }

  const submit = (sendAt?: Date) =>
    form.handleSubmit((values) => {
      if (!editor || signatures.isPending) return
      if (links.some((link) => link.uploading)) {
        toast.error("Wait for the download links to finish uploading.")
        return
      }
      if (links.some((link) => !link.id || link.error)) {
        toast.error("A download link did not upload. Try it again, or remove it.")
        return
      }
      if (links.some((link) => link.password && (link.password.length < 8 || link.password.length > 128))) {
        toast.error("Use a download password between 8 and 128 characters.")
        return
      }
      send.mutate({ values, sendAt })
    })

  const recipientField = (name: "to" | "cc" | "bcc", label: string) => (
    <Controller
      control={form.control}
      name={name}
      render={({ field, fieldState }) => (
        <Field id={`compose-${name}`} label={label} error={fieldState.error?.message ?? fieldState.error?.root?.message}>
          <RecipientInput id={`compose-${name}`} value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder={t("common.emailPlaceholder")} suggest />
        </Field>
      )}
    />
  )

  return (
    <Sheet open onOpenChange={(open) => !open && close()}>
      <SheetContent
        side="right"
        className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[800px]"
        onOpenAutoFocus={(e) => {
          e.preventDefault()
          document.getElementById("compose-to")?.focus()
        }}
      >
        <SheetHeader className="border-b border-border p-6">
          <SheetTitle>{t(`mail.composer.titles.${draft.mode}`)}</SheetTitle>
          {identities.length > 1 ? (
            <SheetDescription asChild>
              <div className="flex items-center gap-2">
                {t("mail.composer.from")}
                <Select value={from.address} onValueChange={changeFrom} disabled={saveDraft.isPending || send.isPending}>
                  <SelectTrigger aria-label={t("mail.composer.from")} size="sm" className="min-w-0">
                    <SelectValue>
                      <span className="min-w-0 truncate">{formatAddress(from)}</span>
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent position="popper" align="start" className="max-w-(--radix-select-content-available-width)">
                    {identities.map(({ mailbox: m, from }) => (
                      <SelectItem key={from.address} value={from.address} textValue={formatAddress(from)}>
                        <span className="flex min-w-0 flex-col">
                          <span className="truncate">{from.name || from.address}</span>
                          <span className="truncate text-sm text-fg-tertiary">{[from.name && from.address, !m.sendAs && m.sendOnBehalf && "On behalf"].filter(Boolean).join(" · ")}</span>
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </SheetDescription>
          ) : (
            <SheetDescription>From {formatAddress(from)}</SheetDescription>
          )}
        </SheetHeader>
        <form
          onSubmit={submit()}
          noValidate
          onDragOver={(e) => {
            if (guard === "password" || !e.dataTransfer.types.includes("Files")) return
            e.preventDefault()
            setDragging(true)
          }}
          onDragLeave={(e) => e.currentTarget === e.target && setDragging(false)}
          onDrop={(e) => {
            if (guard === "password" || !e.dataTransfer.files.length) return
            e.preventDefault()
            setDragging(false)
            addFiles(e.dataTransfer.files)
          }}
          className={`flex min-h-0 flex-1 flex-col transition-colors duration-160 ${dragging ? "bg-primary/5" : ""}`}
        >
          <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-6">
            {recipientField("to", t("mail.composer.to"))}
            {guard !== "password" && (showCopies ? (
              <>
                {recipientField("cc", t("mail.composer.cc"))}
                {recipientField("bcc", t("mail.composer.bcc"))}
              </>
            ) : (
              <Button type="button" variant="link" className="self-start" onClick={() => setShowCopies(true)}>
                {t("mail.composer.addCcBcc")}
              </Button>
            ))}
            <Field id="compose-subject" label={t("mail.composer.subject")}>
              <Input id="compose-subject" autoComplete="off" {...form.register("subject")} />
            </Field>
            <ProtectionControls
              policy={policy.data}
              mode={guard}
              protocol={activeProtocol}
              password={secret}
              hours={hours}
              autocrypt={autocrypt}
              canPassword={mailbox.kind === "user" && mailbox.access === "owner"}
              onMode={(next) => {
                if ((next === "sign" || next === "encrypt") && policy.data?.openpgp === "off") setProtocol("smime")
                if ((next === "sign" || next === "encrypt") && policy.data?.smime === "off") setProtocol("openpgp")
                void chooseProtection(next)
              }}
              onProtocol={setProtocol}
              onPassword={setSecret}
              onHours={setHours}
              onAutocrypt={setAutocrypt}
            />
            {(files.length > 0 || forwarded.length > 0) && (
              <ul aria-label={t("mail.composer.attachments")} className="flex flex-wrap gap-2">
                {forwarded.map((a) => (
                  <AttachmentChip key={a.part} name={a.filename} size={a.size} onRemove={() => {
                    setForwarded(forwarded.filter((x) => x.part !== a.part))
                    touch()
                  }} />
                ))}
                {files.map((f, i) => (
                  <AttachmentChip
                    key={`${f.name}-${i}`}
                    name={f.name}
                    size={f.size}
                    action={
                      <Button type="button" variant="link" className="shrink-0 text-xs" onClick={() => sendFileAsLink(f, i)}>
                        Send as link
                      </Button>
                    }
                    onRemove={() => {
                      setFiles(files.filter((_, j) => j !== i))
                      touch()
                    }}
                  />
                ))}
              </ul>
            )}
            {links.length > 0 && (
              <ul aria-label="Download links" className="flex flex-col gap-3">
                {links.map((link) => (
                  <li key={link.localId} className="flex flex-col gap-3 rounded-lg border border-strong bg-card p-3">
                    <div className="flex min-h-10 items-center gap-2 text-sm">
                      <Link2 aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
                      <span className="min-w-0 flex-1 truncate">{link.filename}</span>
                      <span className="shrink-0 text-xs text-fg-tertiary">{formatBytes(link.size)}</span>
                      <IconButton type="button" label={`Remove ${link.filename}`} size="icon-sm" onClick={() => removeLink(link)}>
                        <X />
                      </IconButton>
                    </div>
                    <p className="text-sm text-fg-secondary">Download link</p>
                    {link.uploading && <p role="status" className="text-sm text-fg-tertiary">Uploading {link.filename}…</p>}
                    {link.error && (
                      <p role="alert" className="text-sm text-danger">
                        {link.error}{" "}
                        {link.file && (
                          <Button type="button" variant="link" onClick={() => uploadLink(link.localId, link.file!)}>
                            Try again
                          </Button>
                        )}
                      </p>
                    )}
                    {link.id && (
                      <div className="grid gap-3 sm:grid-cols-2">
                        <Field
                          id={`link-password-${link.localId}`}
                          label="Password"
                          hint={link.passwordProtected && !link.password && !link.clearPassword ? "Leave blank to keep the current password. It is not included in the email." : "Optional. Tell the recipient separately. It is not included in the email."}
                        >
                          <Input
                            id={`link-password-${link.localId}`}
                            type="password"
                            autoComplete="new-password"
                            value={link.password}
                            maxLength={128}
                            onChange={(e) => {
                              const password = e.target.value
                              setLinks((current) => current.map((item) => (item.localId === link.localId ? { ...item, password, clearPassword: false } : item)))
                              touch()
                            }}
                          />
                        </Field>
                        <Field id={`link-expiry-${link.localId}`} label="Expires" hint="Optional. The link stops working at this time.">
                          <Input
                            id={`link-expiry-${link.localId}`}
                            type="datetime-local"
                            value={link.expires}
                            onChange={(e) => {
                              const expires = e.target.value
                              setLinks((current) => current.map((item) => (item.localId === link.localId ? { ...item, expires } : item)))
                              touch()
                            }}
                          />
                        </Field>
                      </div>
                    )}
                    {link.passwordProtected && link.id && (
                      <Button
                        type="button"
                        variant="link"
                        className="self-start"
                        onClick={() => {
                          setLinks((current) => current.map((item) => (item.localId === link.localId ? { ...item, password: "", clearPassword: !item.clearPassword } : item)))
                          touch()
                        }}
                      >
                        {link.clearPassword ? "Keep the current password" : "Remove password"}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
            <div className="focus-ring-within flex flex-col rounded-lg border border-input bg-sunken">
              {editor && <FormatToolbar editor={editor} signatures={signatures.data?.signatures} />}
              <EditorContent editor={editor} />
            </div>
          </div>
          <div className="flex flex-wrap items-center justify-end gap-3 border-t border-border p-6">
            <input ref={fileInput} type="file" multiple hidden onChange={(e) => {
              addFiles(e.target.files)
              e.target.value = ""
            }} />
            <p className="basis-full text-sm text-fg-tertiary">Files larger than {formatBytes(limits.linkThresholdBytes)} are sent as download links, with an optional password and expiry. Unsent links are kept for 30 days.</p>
            <div className="mr-auto flex items-center gap-3">
              {guard !== "password" && (
                <Button type="button" variant="ghost" onClick={() => fileInput.current?.click()}>
                  <Paperclip aria-hidden />
                  {t("mail.composer.attach")}
                </Button>
              )}
              {inlineBytes > 0 && <span className="text-sm whitespace-nowrap text-fg-tertiary">{formatBytes(inlineBytes)} of {formatBytes(limits.attachmentBytes)} attached</span>}
              {links.length > 0 && <span className="text-sm whitespace-nowrap text-fg-tertiary">{links.length === 1 ? "1 download link" : `${links.length} download links`}</span>}
              {signatures.isError && <Button type="button" variant="link" onClick={() => signatures.refetch()}>Retry loading signature</Button>}
              <span role="status" className="text-sm whitespace-nowrap text-fg-tertiary">
                {guard !== "standard" ? "Not saved as a draft" : saveDraft.isPending ? "Saving…" : saveDraft.isError ? "Couldn't save the draft" : savedAt ? `Saved ${formatListDate(savedAt.toISOString())}` : ""}
              </span>
              {undoSeconds > 0 && (
                <span id="compose-undo-hint" className="text-sm text-fg-tertiary">
                  Undo for {undoSeconds} seconds after sending
                </span>
              )}
            </div>
            <Button type="button" variant="secondary" loading={discard.isPending} onClick={() => discard.mutate()}>
              {t("mail.composer.discard")}
            </Button>
            <div className="flex">
              <Button type="submit" loading={send.isPending} disabled={signatures.isPending} className={guard === "password" ? undefined : "rounded-r-none hover:shadow-none"} aria-describedby={undoSeconds > 0 ? "compose-undo-hint" : undefined}>
                <Send ref={sendIcon} aria-hidden />
                {t("mail.composer.send")}
              </Button>
              {guard !== "password" && (
                <LaterMenu label="Schedule send" submitLabel="Schedule" disabled={send.isPending || signatures.isPending} onPick={(at) => submit(at)()}>
                  <Button type="button" size="icon" aria-label={t("mail.composer.scheduleSend")} className="h-auto self-stretch rounded-l-none border-l-ink/20 hover:shadow-none">
                    <ChevronDown />
                  </Button>
                </LaterMenu>
              )}
            </div>
          </div>
        </form>
        <Dialog open={!!revealed} onOpenChange={(open) => !open && onClose()}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Share this password separately</DialogTitle>
              <DialogDescription>The recipient gets a link by email. It opens only with this password, and Aether cannot show it again.</DialogDescription>
            </DialogHeader>
            <CopyField id="shared-password" label="Password" value={revealed} copyLabel="Copy password" />
            <DialogFooter>
              <Button type="button" onClick={onClose}>Done</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </SheetContent>
    </Sheet>
  )
}

"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { EditorContent, useEditor, useEditorState, type Editor } from "@tiptap/react"
import StarterKit from "@tiptap/starter-kit"
import { Bold, ChevronDown, Italic, Link2, List, ListOrdered, Paperclip, Send, X } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { Field } from "@/components/field"
import { IconButton } from "@/components/icon-button"
import { LaterMenu } from "@/components/mail/later-menu"
import { RecipientInput } from "@/components/recipient-input"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { api } from "@/lib/api"
import { toRFC3339 } from "@/lib/calendar"
import { formatAddress, formatBytes, formatListDate, formatShortDateTime } from "@/lib/format"
import type { Draft } from "@/lib/mail"
import type { Attachment, Mailbox, OutgoingMessage, SavedDraft } from "@/lib/types"

const address = z.object({ name: z.string().optional(), address: z.email() })

const schema = z.object({
  to: z.array(address).min(1, "Add at least one recipient."),
  cc: z.array(address),
  bcc: z.array(address),
  subject: z.string(),
})

type Values = z.infer<typeof schema>

const maxAttachmentBytes = 20 * 1024 * 1024

function readBase64(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result).slice(String(reader.result).indexOf(",") + 1))
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

function AttachmentChip({ name, size, onRemove }: { name: string; size: number; onRemove: () => void }) {
  return (
    <li className="flex min-h-10 max-w-[280px] items-center gap-2 rounded-lg border border-strong py-1 pr-1 pl-3 text-sm">
      <Paperclip aria-hidden className="size-4 shrink-0 text-fg-tertiary" />
      <span className="truncate">{name}</span>
      <span className="shrink-0 text-xs text-fg-tertiary">{formatBytes(size)}</span>
      <IconButton type="button" label={`Remove ${name}`} size="icon-sm" onClick={onRemove}>
        <X />
      </IconButton>
    </li>
  )
}

const titles: Record<Draft["mode"], string> = { new: "New message", reply: "Reply", replyAll: "Reply all", forward: "Forward", draft: "Draft" }

function Toolbar({ editor }: { editor: Editor }) {
  const state = useEditorState({
    editor,
    selector: ({ editor }) => ({
      bold: editor.isActive("bold"),
      italic: editor.isActive("italic"),
      bulletList: editor.isActive("bulletList"),
      orderedList: editor.isActive("orderedList"),
      link: editor.isActive("link"),
    }),
  })

  function setLink() {
    const previous = editor.getAttributes("link").href as string | undefined
    const url = window.prompt("Link address", previous ?? "https://")
    if (url === null) return
    if (url.trim() === "") editor.chain().focus().extendMarkRange("link").unsetLink().run()
    else editor.chain().focus().extendMarkRange("link").setLink({ href: url.trim() }).run()
  }

  const buttons = [
    { label: "Bold", icon: Bold, active: state.bold, run: () => editor.chain().focus().toggleBold().run() },
    { label: "Italic", icon: Italic, active: state.italic, run: () => editor.chain().focus().toggleItalic().run() },
    { label: "Bulleted list", icon: List, active: state.bulletList, run: () => editor.chain().focus().toggleBulletList().run() },
    { label: "Numbered list", icon: ListOrdered, active: state.orderedList, run: () => editor.chain().focus().toggleOrderedList().run() },
    { label: "Link", icon: Link2, active: state.link, run: setLink },
  ]

  return (
    <div role="toolbar" aria-label="Formatting" className="flex gap-1 border-b border-border p-1">
      {buttons.map((b) => (
        <IconButton key={b.label} type="button" label={b.label} size="icon-sm" aria-pressed={b.active} onClick={b.run} className={b.active ? "bg-muted text-foreground" : undefined}>
          <b.icon />
        </IconButton>
      ))}
    </div>
  )
}

export function Composer({ mailbox, draft, onClose }: { mailbox: Mailbox; draft: Draft; onClose: () => void }) {
  const qc = useQueryClient()
  const [showCopies, setShowCopies] = useState(draft.cc.length > 0)
  const [files, setFiles] = useState<File[]>([])
  const [forwarded, setForwarded] = useState<Attachment[]>(draft.forwardFrom?.attachments ?? [])
  const [source, setSource] = useState(draft.forwardFrom ? { folder: draft.forwardFrom.folder, uid: draft.forwardFrom.uid } : null)
  const [draftUid, setDraftUid] = useState(draft.draftUid)
  const [savedAt, setSavedAt] = useState<Date | null>(null)
  const [changes, setChanges] = useState(0)
  const dirty = useRef(false)
  const pendingSave = useRef<Promise<unknown> | null>(null)
  const [dragging, setDragging] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const totalBytes = files.reduce((n, f) => n + f.size, 0) + forwarded.reduce((n, a) => n + a.size, 0)

  const touch = useCallback(() => {
    dirty.current = true
    setChanges((n) => n + 1)
  }, [])

  function addFiles(list: FileList | null) {
    if (!list?.length) return
    const next = [...files, ...Array.from(list)]
    const size = next.reduce((n, f) => n + f.size, 0) + forwarded.reduce((n, a) => n + a.size, 0)
    if (next.length + forwarded.length > 20 || size > maxAttachmentBytes) {
      toast.error("Attachments can total at most 20 files and 20 MB per message.")
      return
    }
    setFiles(next)
    touch()
  }
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { to: draft.to, cc: draft.cc, bcc: draft.bcc, subject: draft.subject },
  })
  const editor = useEditor({
    extensions: [StarterKit.configure({ heading: false, link: { openOnClick: false, defaultProtocol: "https" } })],
    content: draft.html,
    immediatelyRender: false,
    onUpdate: touch,
    autofocus: draft.to.length ? "start" : false,
    editorProps: {
      attributes: {
        class: "prose-editor min-h-[240px] px-4 py-3 text-base outline-none",
        role: "textbox",
        "aria-multiline": "true",
        "aria-label": "Message body",
        "data-focus-ring": "off",
      },
    },
  })

  async function payload(values: Values, uploads: File[]): Promise<OutgoingMessage> {
    const attachments = await Promise.all(uploads.map(async (f) => ({ filename: f.name, contentType: f.type || "application/octet-stream", data: await readBase64(f) })))
    return {
      ...values,
      html: editor?.getHTML() ?? "",
      text: editor?.getText({ blockSeparator: "\n\n" }) ?? "",
      inReplyTo: draft.inReplyTo,
      references: draft.references?.length ? draft.references : undefined,
      attachments: attachments.length ? attachments : undefined,
      forwardFrom: source && forwarded.length ? { ...source, parts: forwarded.map((a) => a.part) } : undefined,
      draftUid,
    }
  }

  const saveDraft = useMutation({
    mutationFn: async (uploads: File[]) => api<SavedDraft>(`/mailboxes/${mailbox.id}/drafts`, { method: "PUT", body: await payload(form.getValues(), uploads) }),
    meta: { silent: true },
    onSuccess: (saved, uploads) => {
      setDraftUid(saved.uid)
      setSource({ folder: saved.folder, uid: saved.uid })
      setForwarded(saved.attachments)
      setFiles((current) => current.filter((f) => !uploads.includes(f)))
      setSavedAt(new Date())
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
    },
    onError: () => {
      dirty.current = true
    },
  })

  const save = useCallback(() => {
    if (pendingSave.current || !dirty.current) return pendingSave.current
    dirty.current = false
    pendingSave.current = saveDraft.mutateAsync(files).catch(() => undefined).finally(() => {
      pendingSave.current = null
    })
    return pendingSave.current
  }, [files, saveDraft])

  useEffect(() => form.subscribe({ formState: { values: true }, callback: touch }), [form, touch])

  useEffect(() => {
    if (!changes) return
    const timer = setTimeout(save, 2500)
    return () => clearTimeout(timer)
  }, [changes, save])

  const send = useMutation({
    mutationFn: async ({ values, sendAt }: { values: Values; sendAt?: Date }) => {
      await pendingSave.current
      return api(`/mailboxes/${mailbox.id}/send`, { method: "POST", body: { ...(await payload(values, files)), sendAt: sendAt && toRFC3339(sendAt) } })
    },
    onSuccess: (_d, { sendAt }) => {
      toast.success(sendAt ? `Scheduled for ${formatShortDateTime(sendAt)}` : "Message sent")
      qc.invalidateQueries({ queryKey: ["scheduled", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      onClose()
    },
  })

  const discard = useMutation({
    mutationFn: async () => {
      await pendingSave.current
      if (draftUid) await api(`/mailboxes/${mailbox.id}/drafts/${draftUid}`, { method: "DELETE" })
    },
    onSuccess: () => {
      if (draftUid) toast.success("Draft discarded")
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
      onClose()
    },
  })

  async function close() {
    dirty.current = dirty.current || files.length > 0
    await save()
    if (draftUid || savedAt || saveDraft.data) toast.success("Saved to Drafts")
    onClose()
  }

  const submit = (sendAt?: Date) =>
    form.handleSubmit((values) => {
      if (!editor) return
      send.mutate({ values, sendAt })
    })

  const recipientField = (name: "to" | "cc" | "bcc", label: string) => (
    <Controller
      control={form.control}
      name={name}
      render={({ field, fieldState }) => (
        <Field id={`compose-${name}`} label={label} error={fieldState.error?.message ?? fieldState.error?.root?.message}>
          <RecipientInput id={`compose-${name}`} value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" suggest />
        </Field>
      )}
    />
  )

  return (
    <Sheet open onOpenChange={(open) => !open && close()}>
      <SheetContent side="right" className="gap-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[800px]">
        <SheetHeader className="border-b border-border p-6">
          <SheetTitle>{titles[draft.mode]}</SheetTitle>
          <SheetDescription>From {formatAddress({ name: mailbox.displayName, address: mailbox.address })}</SheetDescription>
        </SheetHeader>
        <form
          onSubmit={submit()}
          noValidate
          onDragOver={(e) => {
            if (!e.dataTransfer.types.includes("Files")) return
            e.preventDefault()
            setDragging(true)
          }}
          onDragLeave={(e) => e.currentTarget === e.target && setDragging(false)}
          onDrop={(e) => {
            if (!e.dataTransfer.files.length) return
            e.preventDefault()
            setDragging(false)
            addFiles(e.dataTransfer.files)
          }}
          className={`flex min-h-0 flex-1 flex-col transition-colors duration-160 ${dragging ? "bg-primary/5" : ""}`}
        >
          <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-6">
            {recipientField("to", "To")}
            {showCopies ? (
              <>
                {recipientField("cc", "Cc")}
                {recipientField("bcc", "Bcc")}
              </>
            ) : (
              <Button type="button" variant="link" className="self-start" onClick={() => setShowCopies(true)}>
                Add Cc or Bcc
              </Button>
            )}
            <Field id="compose-subject" label="Subject">
              <Input id="compose-subject" autoComplete="off" {...form.register("subject")} />
            </Field>
            <div className="focus-ring-within flex flex-col rounded-lg border border-input bg-sunken">
              {editor && <Toolbar editor={editor} />}
              <EditorContent editor={editor} />
            </div>
            {(files.length > 0 || forwarded.length > 0) && (
              <ul aria-label="Attachments" className="flex flex-wrap gap-2">
                {forwarded.map((a) => (
                  <AttachmentChip key={a.part} name={a.filename} size={a.size} onRemove={() => {
                    setForwarded(forwarded.filter((x) => x.part !== a.part))
                    touch()
                  }} />
                ))}
                {files.map((f, i) => (
                  <AttachmentChip key={`${f.name}-${i}`} name={f.name} size={f.size} onRemove={() => {
                    setFiles(files.filter((_, j) => j !== i))
                    touch()
                  }} />
                ))}
              </ul>
            )}
          </div>
          <div className="flex flex-wrap items-center justify-end gap-3 border-t border-border p-6">
            <input ref={fileInput} type="file" multiple hidden onChange={(e) => {
              addFiles(e.target.files)
              e.target.value = ""
            }} />
            <div className="mr-auto flex items-center gap-3">
              <Button type="button" variant="ghost" onClick={() => fileInput.current?.click()}>
                <Paperclip aria-hidden />
                Attach
              </Button>
              {totalBytes > 0 && <span className="text-sm whitespace-nowrap text-fg-tertiary">{formatBytes(totalBytes)} of 20 MB</span>}
              <span role="status" className="text-sm whitespace-nowrap text-fg-tertiary">
                {saveDraft.isPending ? "Saving…" : saveDraft.isError ? "Couldn't save the draft" : savedAt ? `Saved ${formatListDate(savedAt.toISOString())}` : ""}
              </span>
            </div>
            <Button type="button" variant="secondary" loading={discard.isPending} onClick={() => discard.mutate()}>
              Discard
            </Button>
            <div className="flex">
              <Button type="submit" loading={send.isPending} className="rounded-r-none hover:shadow-none">
                <Send aria-hidden />
                Send
              </Button>
              <LaterMenu label="Schedule send" submitLabel="Schedule" disabled={send.isPending} onPick={(at) => submit(at)()}>
                <Button type="button" size="icon" aria-label="Schedule send" className="h-auto self-stretch rounded-l-none border-l-ink/20 hover:shadow-none">
                  <ChevronDown />
                </Button>
              </LaterMenu>
            </div>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  )
}

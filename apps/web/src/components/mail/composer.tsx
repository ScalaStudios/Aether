"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { EditorContent, useEditor, useEditorState, type Editor } from "@tiptap/react"
import StarterKit from "@tiptap/starter-kit"
import { Bold, Italic, Link2, List, ListOrdered, Send } from "lucide-react"
import { useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { Field } from "@/components/field"
import { IconButton } from "@/components/icon-button"
import { RecipientInput } from "@/components/recipient-input"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { api } from "@/lib/api"
import { formatAddress } from "@/lib/format"
import type { Draft } from "@/lib/mail"
import type { Mailbox, OutgoingMessage } from "@/lib/types"

const address = z.object({ name: z.string().optional(), address: z.email() })

const schema = z.object({
  to: z.array(address).min(1, "Add at least one recipient."),
  cc: z.array(address),
  bcc: z.array(address),
  subject: z.string(),
})

type Values = z.infer<typeof schema>

const titles: Record<Draft["mode"], string> = { new: "New message", reply: "Reply", replyAll: "Reply all", forward: "Forward" }

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
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { to: draft.to, cc: draft.cc, bcc: draft.bcc, subject: draft.subject },
  })
  const editor = useEditor({
    extensions: [StarterKit.configure({ heading: false, link: { openOnClick: false, defaultProtocol: "https" } })],
    content: draft.html,
    immediatelyRender: false,
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

  const send = useMutation({
    mutationFn: (body: OutgoingMessage) => api(`/mailboxes/${mailbox.id}/send`, { method: "POST", body }),
    onSuccess: () => {
      toast.success("Message sent")
      qc.invalidateQueries({ queryKey: ["folders", mailbox.id] })
      qc.invalidateQueries({ queryKey: ["messages", mailbox.id] })
      onClose()
    },
  })

  const submit = form.handleSubmit((values) => {
    if (!editor) return
    send.mutate({
      ...values,
      html: editor.getHTML(),
      text: editor.getText({ blockSeparator: "\n\n" }),
      inReplyTo: draft.inReplyTo,
      references: draft.references?.length ? draft.references : undefined,
    })
  })

  const recipientField = (name: "to" | "cc" | "bcc", label: string) => (
    <Controller
      control={form.control}
      name={name}
      render={({ field, fieldState }) => (
        <Field id={`compose-${name}`} label={label} error={fieldState.error?.message ?? fieldState.error?.root?.message}>
          <RecipientInput id={`compose-${name}`} value={field.value} onChange={field.onChange} invalid={!!fieldState.error} placeholder="name@example.com" />
        </Field>
      )}
    />
  )

  return (
    <Sheet open onOpenChange={(open) => !open && onClose()}>
      <SheetContent side="right" className="w-full gap-0 sm:max-w-[800px]">
        <SheetHeader className="border-b border-border p-6">
          <SheetTitle>{titles[draft.mode]}</SheetTitle>
          <SheetDescription>From {formatAddress({ name: mailbox.displayName, address: mailbox.address })}</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} noValidate className="flex min-h-0 flex-1 flex-col">
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
          </div>
          <div className="flex justify-end gap-3 border-t border-border p-6">
            <Button type="button" variant="secondary" onClick={onClose}>
              Discard
            </Button>
            <Button type="submit" loading={send.isPending}>
              <Send aria-hidden />
              Send
            </Button>
          </div>
        </form>
      </SheetContent>
    </Sheet>
  )
}

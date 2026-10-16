"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { EditorContent, useEditor } from "@tiptap/react"
import { LayoutTemplate, Pencil, Plus, Signature as SignatureIcon, Trash2 } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { IconButton } from "@/components/icon-button"
import { FormatToolbar, mailEditorExtensions } from "@/components/mail/format-toolbar"
import { SectionHeader } from "@/components/page-header"
import { MailboxPicker } from "@/components/settings/mail-rules"
import { EmptyState } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useSession } from "@/hooks/use-session"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api } from "@/lib/api"
import { myMailboxesQuery, signaturesQuery } from "@/lib/queries"
import type { Mailbox, Signature, SignatureSettings } from "@/lib/types"

export function Signatures() {
  const mailboxes = useQuery({ ...myMailboxesQuery, select: (list) => list.filter((m) => m.access !== "read") })
  const [chosen, setChosen] = useState<string | null>(null)
  const mailboxId = chosen ?? mailboxes.data?.[0]?.id ?? null
  const [editing, setEditing] = useState<Signature | null>(null)

  return (
    <section id="signatures" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader
        title="Signatures"
        description="Added to messages you write in Aether Mail. Mail apps on your phone or computer keep their own signature settings."
        actions={
          <>
            <MailboxPicker mailboxes={mailboxes.data} value={mailboxId} onChange={setChosen} />
            {mailboxId && (
              <Button onClick={() => setEditing({ id: "", name: "", html: "" })}>
                <Plus aria-hidden />
                New signature
              </Button>
            )}
          </>
        }
      />
      {mailboxId && <SignatureList key={mailboxId} mailboxId={mailboxId} editing={editing} setEditing={setEditing} />}
    </section>
  )
}

function SignatureList({ mailboxId, editing, setEditing }: { mailboxId: string; editing: Signature | null; setEditing: (s: Signature | null) => void }) {
  const qc = useQueryClient()
  const query = useQuery(signaturesQuery(mailboxId))
  const [deleting, setDeleting] = useState<Signature | null>(null)
  const save = useMutation({
    mutationFn: (next: SignatureSettings) => api<SignatureSettings>(`/mailboxes/${mailboxId}/signatures`, { method: "PUT", body: next }),
    onSuccess: (saved) => qc.setQueryData(signaturesQuery(mailboxId).queryKey, saved),
  })
  const settings = query.data

  function upsert(sig: Signature) {
    if (!settings) return
    const id = sig.id || crypto.randomUUID()
    const exists = settings.signatures.some((s) => s.id === id)
    const signatures = exists ? settings.signatures.map((s) => (s.id === id ? { ...sig, id } : s)) : [...settings.signatures, { ...sig, id }]
    const first = settings.signatures.length === 0
    save.mutate(
      { signatures, newMessage: first ? id : settings.newMessage, replies: first ? id : settings.replies },
      {
        onSuccess: () => {
          toast.success(exists ? "Signature updated" : "Signature created")
          setEditing(null)
        },
      }
    )
  }

  function remove(sig: Signature) {
    if (!settings) return
    const unset = (id: string) => (id === sig.id ? "" : id)
    save.mutate(
      { signatures: settings.signatures.filter((s) => s.id !== sig.id), newMessage: unset(settings.newMessage), replies: unset(settings.replies) },
      {
        onSuccess: () => {
          toast.success("Signature deleted")
          setDeleting(null)
        },
      }
    )
  }

  if (!settings) return null

  return (
    <>
      {settings.signatures.length === 0 ? (
        <EmptyState icon={SignatureIcon} title="No signatures" description="Create one to sign new messages and replies automatically." />
      ) : (
        <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
          <div className="grid gap-4 sm:grid-cols-2">
            <DefaultPicker id="sig-new" label="New messages" value={settings.newMessage} signatures={settings.signatures} onChange={(v) => save.mutate({ ...settings, newMessage: v })} />
            <DefaultPicker id="sig-replies" label="Replies and forwards" value={settings.replies} signatures={settings.signatures} onChange={(v) => save.mutate({ ...settings, replies: v })} />
          </div>
          <ul className="flex flex-col divide-y divide-border">
            {settings.signatures.map((sig) => (
              <li key={sig.id} className="flex items-start gap-4 py-4 first:pt-0 last:pb-0">
                <div className="flex min-w-0 flex-1 flex-col gap-2">
                  <span className="font-semibold">{sig.name}</span>
                  <div className="prose-editor max-h-32 overflow-hidden text-sm text-fg-secondary" dangerouslySetInnerHTML={{ __html: sig.html }} />
                </div>
                <IconButton label={`Edit ${sig.name}`} size="icon-sm" onClick={() => setEditing(sig)}>
                  <Pencil />
                </IconButton>
                <IconButton label={`Delete ${sig.name}`} size="icon-sm" onClick={() => setDeleting(sig)}>
                  <Trash2 />
                </IconButton>
              </li>
            ))}
          </ul>
        </div>
      )}
      {editing && <SignatureDialog key={editing.id || "new"} mailboxId={mailboxId} signature={editing} pending={save.isPending} onSave={upsert} onClose={() => setEditing(null)} />}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title="Delete signature?"
        description={`“${deleting?.name ?? ""}” will no longer be added to new messages or replies.`}
        confirmLabel="Delete"
        pending={save.isPending}
        onConfirm={() => deleting && remove(deleting)}
      />
    </>
  )
}

function DefaultPicker({ id, label, value, signatures, onChange }: { id: string; label: string; value: string; signatures: Signature[]; onChange: (id: string) => void }) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value || "none"} onValueChange={(v) => onChange(v === "none" ? "" : v)}>
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="none">No signature</SelectItem>
          {signatures.map((s) => (
            <SelectItem key={s.id} value={s.id}>
              {s.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

async function templateHTML(mailbox: Mailbox, org: string) {
  const logo = await fetch("/brand/scala-signature.png")
    .then((r) => r.blob())
    .then((blob) => new Promise<string>((resolve) => {
      const reader = new FileReader()
      reader.onload = () => resolve(reader.result as string)
      reader.readAsDataURL(blob)
    }))
  const domain = mailbox.address.split("@")[1]
  const muted = "color: #6b7280"
  return `<table style="border-collapse: collapse"><tbody><tr>
<td style="vertical-align: top; padding: 0 16px 0 0"><img src="${logo}" width="61" alt="${org}"></td>
<td style="vertical-align: top; padding: 0 0 0 16px; border-left: 1px solid #d1d5db">
<p><strong><span style="font-size: 16px">${mailbox.displayName || mailbox.localPart}</span></strong><br><span style="${muted}">Your title</span><br><strong>${org}</strong></p>
<p><a href="mailto:${mailbox.address}">${mailbox.address}</a><br><a href="https://${domain}">${domain}</a></p>
<p><span style="font-size: 12px; ${muted}">LinkedIn · GitHub · X</span></p>
</td></tr></tbody></table>`
}

function SignatureDialog({ mailboxId, signature, pending, onSave, onClose }: { mailboxId: string; signature: Signature; pending: boolean; onSave: (s: Signature) => void; onClose: () => void }) {
  const [name, setName] = useState(signature.name)
  const [error, setError] = useState("")
  const mailbox = useQuery({ ...myMailboxesQuery, select: (list) => list.find((m) => m.id === mailboxId) }).data
  const org = useSession().data?.memberships.find((m) => m.orgId === mailbox?.orgId)?.orgName ?? ""
  const editor = useEditor({
    extensions: mailEditorExtensions,
    content: signature.html,
    immediatelyRender: false,
    editorProps: { attributes: { class: "prose-editor min-h-[160px] px-4 py-3 text-base outline-none", role: "textbox", "aria-multiline": "true", "aria-label": "Signature", "data-focus-ring": "off" } },
  })

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={signature.id ? "Edit signature" : "New signature"}
      submitLabel={signature.id ? "Save signature" : "Create signature"}
      pending={pending}
      wide
      onSubmit={(e) => {
        e.preventDefault()
        if (!name.trim()) return setError("Name the signature, for example “Work”.")
        onSave({ ...signature, name: name.trim(), html: editor?.isEmpty ? "" : (editor?.getHTML() ?? "") })
      }}
    >
      <Field id="sig-name" label="Name" error={error}>
        <Input id="sig-name" value={name} autoComplete="off" aria-invalid={!!error} onChange={(e) => setName(e.target.value)} />
      </Field>
      {mailbox && editor && (
        <Button
          type="button"
          variant="secondary"
          size="sm"
          className="self-start"
          onClick={async () => {
            editor.commands.setContent(await templateHTML(mailbox, org))
            if (!name.trim()) setName(org || "Default")
          }}
        >
          <LayoutTemplate aria-hidden />
          Use {org || "company"} template
        </Button>
      )}
      <div className="focus-ring-within flex flex-col rounded-lg border border-input bg-sunken">
        {editor && <FormatToolbar editor={editor} />}
        <EditorContent editor={editor} />
      </div>
    </FormDialog>
  )
}

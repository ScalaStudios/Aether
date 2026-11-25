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
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useSession } from "@/hooks/use-session"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { api } from "@/lib/api"
import { senderIdentities } from "@/lib/mail"
import { useI18n } from "@/lib/i18n"
import { myMailboxesQuery, signaturesQuery } from "@/lib/queries"
import type { Signature, SignatureSettings } from "@/lib/types"

export function Signatures() {
  const { t } = useI18n()
  const identities = useQuery({ ...myMailboxesQuery, select: (list) => senderIdentities(list.filter((m) => m.access !== "read" && m.protocols.includes("webmail"))) })
  const [chosen, setChosen] = useState<string | null>(null)
  const identity = identities.data?.find((i) => i.from.address === chosen) ?? identities.data?.[0]
  const [editing, setEditing] = useState<Signature | null>(null)

  return (
    <section id="signatures" className="flex scroll-mt-8 flex-col gap-6">
      <SectionHeader
        title={t("settings.signatures.title")}
        description="Each address you send from has its own From name and signatures in Aether Mail. Mail apps on your phone or computer keep their own settings."
        actions={
          identity && (
            <Button onClick={() => setEditing({ id: "", name: "", html: "" })}>
              <Plus aria-hidden />
              {t("settings.signatures.newSignature")}
            </Button>
          )
        }
      />
      {identities.data && identities.data.length > 1 && (
        <Field id="signature-address" label="Address">
          <Select value={identity?.from.address} onValueChange={setChosen}>
            <SelectTrigger id="signature-address" className="w-full sm:max-w-sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {identities.data.map(({ from }) => (
                <SelectItem key={from.address} value={from.address}>
                  {from.address}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
      {identities.isError ? (
        <ErrorState error={identities.error} onRetry={() => identities.refetch()} />
      ) : identities.isPending ? (
        <RowsSkeleton rows={2} />
      ) : identity ? (
        <SignatureList key={`${identity.mailbox.id}/${identity.from.address}`} mailboxId={identity.mailbox.id} fromAddress={identity.from.address} editing={editing} setEditing={setEditing} />
      ) : (
        <EmptyState icon={SignatureIcon} title="No address to send from" description="Signatures belong to the addresses you send from. Ask an administrator for a mailbox, or for Send as on a shared one." />
      )}
    </section>
  )
}

function SignatureList({ mailboxId, fromAddress, editing, setEditing }: { mailboxId: string; fromAddress: string; editing: Signature | null; setEditing: (s: Signature | null) => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const query = useQuery(signaturesQuery(mailboxId, fromAddress))
  const [deleting, setDeleting] = useState<Signature | null>(null)
  const [displayName, setDisplayName] = useState<string | null>(null)
  const save = useMutation({
    mutationFn: (next: SignatureSettings) => api<SignatureSettings>(`/mailboxes/${mailboxId}/signatures`, { method: "PUT", query: { fromAddress }, body: next }),
    onSuccess: (saved) => {
      qc.setQueryData(signaturesQuery(mailboxId, fromAddress).queryKey, saved)
      qc.invalidateQueries({ queryKey: myMailboxesQuery.queryKey })
    },
  })
  const settings = query.data

  function upsert(sig: Signature) {
    if (!settings) return
    const id = sig.id || crypto.randomUUID()
    const exists = settings.signatures.some((s) => s.id === id)
    const signatures = exists ? settings.signatures.map((s) => (s.id === id ? { ...sig, id } : s)) : [...settings.signatures, { ...sig, id }]
    const first = settings.signatures.length === 0
    save.mutate(
      { ...settings, signatures, newMessage: first ? id : settings.newMessage, replies: first ? id : settings.replies },
      {
        onSuccess: () => {
          toast.success(exists ? t("settings.signatures.updatedToast") : t("settings.signatures.createdToast"))
          setEditing(null)
        },
      }
    )
  }

  function remove(sig: Signature) {
    if (!settings) return
    const unset = (id: string) => (id === sig.id ? "" : id)
    save.mutate(
      { ...settings, signatures: settings.signatures.filter((s) => s.id !== sig.id), newMessage: unset(settings.newMessage), replies: unset(settings.replies) },
      {
        onSuccess: () => {
          toast.success(t("settings.signatures.deletedToast"))
          setDeleting(null)
        },
      }
    )
  }

  if (query.isError) return <ErrorState error={query.error} onRetry={() => query.refetch()} />
  if (!settings) return <RowsSkeleton rows={2} />
  const name = displayName ?? settings.displayName ?? ""

  return (
    <>
      <form
        noValidate
        className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6"
        onSubmit={(e) => {
          e.preventDefault()
          save.mutate(
            { ...settings, displayName: name.trim() },
            {
              onSuccess: () => {
                setDisplayName(null)
                toast.success("From name saved")
              },
            }
          )
        }}
      >
        <Field id="sender-name" label="From name" hint={`Recipients see this name next to ${fromAddress}. Leave it empty to use the name your administrator set.`}>
          <Input id="sender-name" maxLength={200} autoComplete="off" className="sm:max-w-sm" value={name} onChange={(e) => setDisplayName(e.target.value)} />
        </Field>
        <Button type="submit" variant="secondary" className="self-start" loading={save.isPending && displayName !== null} disabled={name.trim() === (settings.displayName ?? "")}>
          Save From name
        </Button>
      </form>
      {settings.signatures.length === 0 ? (
        <EmptyState icon={SignatureIcon} title={t("settings.signatures.emptyTitle")} description={t("settings.signatures.emptyDescription")} />
      ) : (
        <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-6">
          <div className="grid gap-4 sm:grid-cols-2">
            <DefaultPicker id="sig-new" label={t("settings.signatures.newMessages")} value={settings.newMessage} signatures={settings.signatures} onChange={(v) => save.mutate({ ...settings, newMessage: v })} />
            <DefaultPicker id="sig-replies" label={t("settings.signatures.repliesAndForwards")} value={settings.replies} signatures={settings.signatures} onChange={(v) => save.mutate({ ...settings, replies: v })} />
          </div>
          <ul className="flex flex-col divide-y divide-border">
            {settings.signatures.map((sig) => (
              <li key={sig.id} className="flex items-start gap-4 py-4 first:pt-0 last:pb-0">
                <div className="flex min-w-0 flex-1 flex-col gap-2">
                  <span className="font-semibold">{sig.name}</span>
                  <div className="prose-editor max-h-32 overflow-hidden text-sm text-fg-secondary" dangerouslySetInnerHTML={{ __html: sig.html }} />
                </div>
                <IconButton label={t("settings.signatures.editAria", { name: sig.name })} size="icon-sm" onClick={() => setEditing(sig)}>
                  <Pencil />
                </IconButton>
                <IconButton label={t("settings.signatures.deleteAria", { name: sig.name })} size="icon-sm" onClick={() => setDeleting(sig)}>
                  <Trash2 />
                </IconButton>
              </li>
            ))}
          </ul>
        </div>
      )}
      {editing && <SignatureDialog key={editing.id || "new"} mailboxId={mailboxId} fromAddress={fromAddress} signature={editing} pending={save.isPending} onSave={upsert} onClose={() => setEditing(null)} />}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("settings.signatures.deleteTitle")}
        description={t("settings.signatures.deleteDescription", { name: deleting?.name ?? "" })}
        confirmLabel={t("common.delete")}
        pending={save.isPending}
        onConfirm={() => deleting && remove(deleting)}
      />
    </>
  )
}

function DefaultPicker({ id, label, value, signatures, onChange }: { id: string; label: string; value: string; signatures: Signature[]; onChange: (id: string) => void }) {
  const { t } = useI18n()
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value || "none"} onValueChange={(v) => onChange(v === "none" ? "" : v)}>
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="none">{t("settings.signatures.noSignature")}</SelectItem>
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

function SignatureDialog({ mailboxId, fromAddress, signature, pending, onSave, onClose }: { mailboxId: string; fromAddress: string; signature: Signature; pending: boolean; onSave: (s: Signature) => void; onClose: () => void }) {
  const { t } = useI18n()
  const [name, setName] = useState(signature.name)
  const [error, setError] = useState("")
  const mailbox = useQuery({ ...myMailboxesQuery, select: (list) => list.find((m) => m.id === mailboxId) }).data
  const org = useSession().data?.memberships.find((m) => m.orgId === mailbox?.orgId)?.orgName ?? ""
  const editor = useEditor({
    extensions: mailEditorExtensions,
    content: signature.html,
    immediatelyRender: false,
    editorProps: { attributes: { class: "prose-editor min-h-[160px] px-4 py-3 text-base outline-none", role: "textbox", "aria-multiline": "true", "aria-label": t("settings.signatures.editorAria"), "data-focus-ring": "off" } },
  })
  const template = useMutation({
    mutationFn: () => api<{ html: string }>(`/mailboxes/${mailboxId}/signature-template`, { query: { fromAddress } }),
    onSuccess: ({ html }) => {
      editor?.commands.setContent(html)
      if (!name.trim()) setName(org || t("settings.signatures.defaultName"))
    },
  })

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={signature.id ? t("settings.signatures.editTitle") : t("settings.signatures.newSignature")}
      submitLabel={signature.id ? t("settings.signatures.saveSubmit") : t("settings.signatures.createSubmit")}
      pending={pending}
      wide
      onSubmit={(e) => {
        e.preventDefault()
        if (!name.trim()) return setError(t("settings.signatures.nameRequired"))
        onSave({ ...signature, name: name.trim(), html: editor?.isEmpty ? "" : (editor?.getHTML() ?? "") })
      }}
    >
      <Field id="sig-name" label={t("common.name")} error={error}>
        <Input id="sig-name" value={name} autoComplete="off" aria-invalid={!!error} onChange={(e) => setName(e.target.value)} />
      </Field>
      {editor && (
        <Button type="button" variant="secondary" size="sm" className="self-start" loading={template.isPending} onClick={() => template.mutate()}>
          <LayoutTemplate aria-hidden />
          {t("settings.signatures.useOrgTemplate", { org: org || t("settings.signatures.companyFallback") })}
        </Button>
      )}
      <div className="focus-ring-within flex flex-col rounded-lg border border-input bg-sunken">
        {editor && <FormatToolbar editor={editor} />}
        <EditorContent editor={editor} />
      </div>
    </FormDialog>
  )
}

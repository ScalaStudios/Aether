"use client"

import { useState } from "react"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useI18n } from "@/lib/i18n"
import { folderLabel, folderParent, snoozedFolder } from "@/lib/mail"
import type { Folder } from "@/lib/types"

export type FolderAction = { kind: "create" | "rename" | "move" | "delete" | "empty"; folder?: Folder }

const topLevel = "/"

export function folderLeaf(folder: Folder) {
  if (folder.specialUse || folder.name === snoozedFolder) return folderLabel(folder)
  return folder.name.slice(folder.name.lastIndexOf("/") + 1)
}

function FolderSelect({ id, value, onChange, folders, topLevelLabel }: { id: string; value: string; onChange: (value: string) => void; folders: Folder[]; topLevelLabel?: string }) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {topLevelLabel && <SelectItem value={topLevel}>{topLevelLabel}</SelectItem>}
        {folders.map((f) => (
          <SelectItem key={f.name} value={f.name}>
            {folderLabel(f)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function FolderDialog({
  action,
  folders,
  pending,
  error,
  onClose,
  onSubmit,
}: {
  action: FolderAction
  folders: Folder[]
  pending: boolean
  error: string
  onClose: () => void
  onSubmit: (values: { name?: string; parent?: string; moveTo?: string }) => void
}) {
  const { t, tp, locale } = useI18n()
  const messages = (count: number) => tp("mail.messageCount", count, { count: count.toLocaleString(locale) })
  const folder = action.folder
  const inside = (f: Folder) => !!folder && (f.name === folder.name || f.name.startsWith(`${folder.name}/`))
  const current = folder ? folderParent(folder.name) : ""
  const targets = folders.filter((f) => !inside(f) && f.name !== current)
  const [name, setName] = useState(action.kind === "rename" && folder ? folderLeaf(folder) : "")
  const [parent, setParent] = useState(current ? topLevel : (targets[0]?.name ?? topLevel))
  const [moveTo, setMoveTo] = useState(folders.find((f) => f.specialUse === "inbox")?.name ?? "INBOX")
  const openChange = (open: boolean) => !open && onClose()
  const errorText = error && (
    <p role="alert" className="text-sm text-danger">
      {error}
    </p>
  )

  if (action.kind === "create" || action.kind === "rename") {
    return (
      <FormDialog
        open
        onOpenChange={openChange}
        title={
          action.kind === "rename" && folder
            ? t("mail.folders.dialog.renameTitle", { name: folderLeaf(folder) })
            : folder
              ? t("mail.folders.dialog.newInTitle", { name: folderLeaf(folder) })
              : t("mail.folders.dialog.newTitle")
        }
        submitLabel={action.kind === "rename" ? t("mail.folders.dialog.renameSubmit") : t("mail.folders.dialog.createSubmit")}
        pending={pending}
        onSubmit={(e) => {
          e.preventDefault()
          onSubmit({ name })
        }}
      >
        <Field id="folder-name" label={t("common.name")} error={error}>
          <Input id="folder-name" value={name} autoComplete="off" maxLength={100} aria-invalid={!!error} onChange={(e) => setName(e.target.value)} />
        </Field>
      </FormDialog>
    )
  }

  if (!folder) return null

  if (action.kind === "move") {
    return (
      <FormDialog
        open
        onOpenChange={openChange}
        title={t("mail.folders.dialog.moveTitle", { name: folderLeaf(folder) })}
        description={t("mail.folders.dialog.moveDescription")}
        submitLabel={t("mail.folders.dialog.moveSubmit")}
        pending={pending}
        onSubmit={(e) => {
          e.preventDefault()
          onSubmit({ parent: parent === topLevel ? "" : parent })
        }}
      >
        <Field id="folder-parent" label={t("mail.folders.dialog.moveInto")} error={error}>
          <FolderSelect id="folder-parent" value={parent} onChange={setParent} folders={targets} topLevelLabel={current ? t("mail.folders.dialog.topLevel") : undefined} />
        </Field>
      </FormDialog>
    )
  }

  if (action.kind === "delete") {
    return (
      <ConfirmDialog
        open
        onOpenChange={openChange}
        title={t("mail.folders.dialog.deleteTitle", { name: folderLeaf(folder) })}
        description={
          folder.total > 0
            ? t("mail.folders.dialog.deleteWithMessages", { messages: messages(folder.total), name: folderLeaf(folder) })
            : t("mail.folders.dialog.deleteEmpty", { name: folderLeaf(folder) })
        }
        confirmLabel={folder.total > 0 ? t("mail.folders.dialog.moveAndDelete") : t("mail.folders.dialog.deleteFolder")}
        pending={pending}
        onConfirm={() => onSubmit({ moveTo: folder.total > 0 ? moveTo : undefined })}
      >
        {folder.total > 0 && (
          <Field id="folder-move-to" label={t("mail.folders.dialog.moveMessagesTo")}>
            <FolderSelect id="folder-move-to" value={moveTo} onChange={setMoveTo} folders={folders.filter((f) => !inside(f))} />
          </Field>
        )}
        {errorText}
      </ConfirmDialog>
    )
  }

  const permanent = folder.specialUse === "trash" || folder.specialUse === "junk"
  return (
    <ConfirmDialog
      open
      onOpenChange={openChange}
      title={t("mail.folders.dialog.emptyTitle", { name: folderLabel(folder) })}
      description={
        permanent
          ? t("mail.folders.dialog.emptyPermanent", { messages: messages(folder.total), name: folderLabel(folder) })
          : t("mail.folders.dialog.emptyToTrash", { messages: messages(folder.total), name: folderLabel(folder) })
      }
      confirmLabel={permanent ? t("mail.folders.dialog.emptyConfirm", { name: folderLabel(folder) }) : t("mail.folders.dialog.moveToTrash")}
      pending={pending}
      onConfirm={() => onSubmit({})}
    >
      {errorText}
    </ConfirmDialog>
  )
}

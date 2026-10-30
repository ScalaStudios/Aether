"use client"

import { useState } from "react"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { folderLabel, folderParent } from "@/lib/mail"
import type { Folder } from "@/lib/types"

export type FolderAction = { kind: "create" | "rename" | "move" | "delete" | "empty"; folder?: Folder }

const topLevel = "/"

export function folderLeaf(folder: Folder) {
  return folder.specialUse === "inbox" ? "Inbox" : folder.name.slice(folder.name.lastIndexOf("/") + 1)
}

function messages(count: number) {
  return count === 1 ? "1 message" : `${count.toLocaleString()} messages`
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
        title={action.kind === "rename" && folder ? `Rename ${folderLeaf(folder)}` : folder ? `New folder in ${folderLeaf(folder)}` : "New folder"}
        submitLabel={action.kind === "rename" ? "Rename" : "Create folder"}
        pending={pending}
        onSubmit={(e) => {
          e.preventDefault()
          onSubmit({ name })
        }}
      >
        <Field id="folder-name" label="Name" error={error}>
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
        title={`Move ${folderLeaf(folder)}`}
        description="Folders inside it move too."
        submitLabel="Move"
        pending={pending}
        onSubmit={(e) => {
          e.preventDefault()
          onSubmit({ parent: parent === topLevel ? "" : parent })
        }}
      >
        <Field id="folder-parent" label="Move into" error={error}>
          <FolderSelect id="folder-parent" value={parent} onChange={setParent} folders={targets} topLevelLabel={current ? "Top level" : undefined} />
        </Field>
      </FormDialog>
    )
  }

  if (action.kind === "delete") {
    return (
      <ConfirmDialog
        open
        onOpenChange={openChange}
        title={`Delete ${folderLeaf(folder)}?`}
        description={
          folder.total > 0
            ? `Its ${messages(folder.total)} will move to the folder you choose, then ${folderLeaf(folder)} is deleted.`
            : `${folderLeaf(folder)} is removed from Aether Mail and from every app connected to this mailbox.`
        }
        confirmLabel={folder.total > 0 ? "Move and delete" : "Delete folder"}
        pending={pending}
        onConfirm={() => onSubmit({ moveTo: folder.total > 0 ? moveTo : undefined })}
      >
        {folder.total > 0 && (
          <Field id="folder-move-to" label="Move messages to">
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
      title={`Empty ${folderLabel(folder)}?`}
      description={
        permanent
          ? `${messages(folder.total)} in ${folderLabel(folder)} will be deleted permanently. This cannot be undone.`
          : `${messages(folder.total)} in ${folderLabel(folder)} will move to Trash.`
      }
      confirmLabel={permanent ? `Empty ${folderLabel(folder)}` : "Move to Trash"}
      pending={pending}
      onConfirm={() => onSubmit({})}
    >
      {errorText}
    </ConfirmDialog>
  )
}

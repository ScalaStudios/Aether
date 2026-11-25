"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import { Field } from "@/components/field"
import { SectionHeader } from "@/components/page-header"
import { SegmentedControl } from "@/components/segmented-control"
import { QueryView, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/input"
import { api } from "@/lib/api"
import type { AttachmentAction, AttachmentPolicy, OrgAttachmentPolicy } from "@/lib/types"

const card = "flex flex-col gap-4 rounded-xl border border-border bg-card p-6"

const actions: { value: AttachmentAction; label: string }[] = [
  { value: "off", label: "Off" },
  { value: "reject", label: "Reject message" },
  { value: "remove", label: "Remove attachment" },
]

const splitList = (text: string) => text.split(/[\s,;]+/).filter(Boolean)

function useSavePolicy(orgId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (policy: AttachmentPolicy) => api<OrgAttachmentPolicy>(`/organizations/${orgId}/attachment-policy`, { method: "PUT", body: policy }),
    onSuccess: (saved) => qc.setQueryData(["org-attachment-policy", orgId], saved),
  })
}

function policyOf({ inbound, outbound, exemptSenders }: OrgAttachmentPolicy): AttachmentPolicy {
  return { inbound, outbound, exemptSenders }
}

export function AttachmentsTab({ orgId }: { orgId: string }) {
  const policy = useQuery({ queryKey: ["org-attachment-policy", orgId], queryFn: () => api<OrgAttachmentPolicy>(`/organizations/${orgId}/attachment-policy`) })

  return (
    <QueryView query={policy} skeleton={<RowsSkeleton rows={4} />}>
      {(data) => (
        <div className="flex flex-col gap-6">
          <Rules
            orgId={orgId}
            policy={data}
            direction="inbound"
            title="Incoming mail"
            description="Applies to mail from other servers to addresses in this organization. Rejected senders get a bounce naming the attachment."
          />
          <Rules
            orgId={orgId}
            policy={data}
            direction="outbound"
            title="Outgoing mail"
            description="Applies to mail sent by people in this organization, from the web app or a mail app. A rejected message isn't sent, and the sender sees why."
          />
          <ExemptSenders orgId={orgId} policy={data} />
        </div>
      )}
    </QueryView>
  )
}

function Rules({
  orgId,
  policy,
  direction,
  title,
  description,
}: {
  orgId: string
  policy: OrgAttachmentPolicy
  direction: "inbound" | "outbound"
  title: string
  description: string
}) {
  const saved = policy[direction]
  const [action, setAction] = useState(saved.action)
  const [text, setText] = useState(saved.extensions.join(", "))
  const save = useSavePolicy(orgId)
  const extensions = splitList(text)
  const unchanged = action === saved.action && extensions.join() === saved.extensions.join()
  const id = `attachments-${direction}`

  return (
    <form
      noValidate
      className={card}
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate(
          { ...policyOf(policy), [direction]: { action, extensions } },
          {
            onSuccess: (next) => {
              setText(next[direction].extensions.join(", "))
              toast.success(`${title} settings saved`)
            },
          }
        )
      }}
    >
      <SectionHeader title={title} description={description} />
      <div className="flex flex-col gap-2">
        <span className="text-label text-fg-secondary">When a message has a blocked file type</span>
        <SegmentedControl label={`${title}: action for blocked file types`} options={actions} value={action} onChange={setAction} />
        <p className="text-sm text-fg-tertiary">
          {action === "off"
            ? "Attachments aren't checked."
            : action === "reject"
              ? "The whole message is refused."
              : "The message is delivered with each blocked attachment replaced by a short note."}
        </p>
      </div>
      <Field
        id={id}
        label="Blocked file types"
        hint="File extensions, separated by commas or spaces. Aether also checks the type detected from each file's content and the names of files inside ZIP, 7z and gzip archives."
      >
        <Textarea id={id} rows={4} spellCheck={false} value={text} className="font-mono text-sm" onChange={(e) => setText(e.target.value)} />
      </Field>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" variant="secondary" loading={save.isPending} disabled={unchanged}>
          Save
        </Button>
        <Button type="button" variant="quiet" disabled={extensions.join() === policy.defaultExtensions.join()} onClick={() => setText(policy.defaultExtensions.join(", "))}>
          Use the default list
        </Button>
      </div>
    </form>
  )
}

function ExemptSenders({ orgId, policy }: { orgId: string; policy: OrgAttachmentPolicy }) {
  const [text, setText] = useState(policy.exemptSenders.join("\n"))
  const save = useSavePolicy(orgId)
  const senders = splitList(text)

  return (
    <form
      noValidate
      className={card}
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate(
          { ...policyOf(policy), exemptSenders: senders },
          {
            onSuccess: (next) => {
              setText(next.exemptSenders.join("\n"))
              toast.success("Exceptions saved")
            },
          }
        )
      }}
    >
      <SectionHeader
        title="Exceptions"
        description="Mail from these senders is never checked for blocked file types, in either direction. Aether matches the envelope sender, which other servers can forge, so only add senders you trust and whose domain publishes SPF or DKIM."
      />
      <Field id="attachments-exempt" label="Senders" hint="One per line. Enter an address such as billing@partner.example, or @partner.example for a whole domain.">
        <Textarea id="attachments-exempt" rows={4} spellCheck={false} value={text} className="font-mono text-sm" onChange={(e) => setText(e.target.value)} />
      </Field>
      <Button type="submit" variant="secondary" className="self-start" loading={save.isPending} disabled={senders.join() === policy.exemptSenders.join()}>
        Save exceptions
      </Button>
    </form>
  )
}

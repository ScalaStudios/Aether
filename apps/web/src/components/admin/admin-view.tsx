"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Building2, Plus } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { AliasesTab } from "@/components/admin/aliases-tab"
import { AttachmentsTab } from "@/components/admin/attachments-tab"
import { AuditTab } from "@/components/admin/audit-tab"
import { BlockedAddressesTab } from "@/components/admin/blocked-addresses-tab"
import { BrandingTab } from "@/components/admin/branding-tab"
import { ContactsTab } from "@/components/admin/contacts-tab"
import { DomainsTab } from "@/components/admin/domains-tab"
import { ForwardingTab } from "@/components/admin/forwarding-tab"
import { EncryptionTab } from "@/components/admin/encryption-tab"
import { IdentityProvidersTab } from "@/components/admin/identity-providers-tab"
import { IntegrationsTab } from "@/components/admin/integrations-tab"
import { MailQueueTab } from "@/components/admin/mail-queue-tab"
import { LimitsTab } from "@/components/admin/limits-tab"
import { MailboxesTab } from "@/components/admin/mailboxes-tab"
import { MailFlowTab } from "@/components/admin/mail-flow-tab"
import { MembersTab } from "@/components/admin/members-tab"
import { QuarantineTab } from "@/components/admin/quarantine-tab"
import { RolesTab } from "@/components/admin/roles-tab"
import { SendersTab } from "@/components/admin/senders-tab"
import { MessageTraceTab } from "@/components/admin/message-trace/trace-tab"
import { RetentionTab } from "@/components/admin/retention-tab"
import { ResourcesTab } from "@/components/admin/resources-tab"
import { SecurityTab } from "@/components/admin/security-tab"
import { OutboundRoutingTab } from "@/components/admin/outbound-routing-tab"
import { SendingTab } from "@/components/admin/sending-tab"
import { SenderWarningsTab } from "@/components/admin/sender-warnings-tab"
import { PrivacyTab } from "@/components/admin/privacy-tab"
import { SignaturesTab } from "@/components/admin/signatures-tab"
import { SpamFilterTab } from "@/components/admin/spam-filter-tab"
import { ImportMail } from "@/components/settings/import-mail"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { PageContainer } from "@/components/page-container"
import { PageHeader } from "@/components/page-header"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { StatusBadge } from "@/components/status-badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { plural } from "@/lib/format"
import { hasPerm, isDomainScoped, orgMembership, roleBadgeTone, roleLabel } from "@/lib/permissions"
import { organizationsQuery } from "@/lib/queries"
import type { Organization, Session } from "@/lib/types"

const orgSchema = z.object({
  name: z.string().trim().min(1, "Enter the organization name."),
  slug: z.string().regex(/^[a-z0-9][a-z0-9-]{0,62}$/, "Use lowercase letters, digits and hyphens, starting with a letter or digit."),
})

type OrgValues = z.infer<typeof orgSchema>

interface OrgTabDef {
  value: string
  label: (session: Session, orgId: string) => string
  visible: (session: Session, orgId: string) => boolean
  Component: React.ComponentType<{ orgId: string }>
}

function orgAdmin(session: Session, orgId: string) {
  const role = orgMembership(session, orgId)?.role
  return session.user.isPlatformAdmin || role === "owner" || role === "admin"
}

const orgTabs: OrgTabDef[] = [
  { value: "domains", label: () => "Domains", visible: (s, id) => hasPerm(s, id, "domains:read"), Component: DomainsTab },
  { value: "mailboxes", label: () => "Mailboxes", visible: (s, id) => hasPerm(s, id, "mailboxes:read"), Component: MailboxesTab },
  { value: "imports", label: () => "Mail imports", visible: (s, id) => orgAdmin(s, id), Component: ImportMail },
  {
    value: "aliases",
    label: (s, id) => (hasPerm(s, id, "aliases:read") ? "Aliases" : "Distribution lists"),
    visible: (s, id) => hasPerm(s, id, "aliases:read", "lists:read"),
    Component: AliasesTab,
  },
  { value: "senders", label: () => "Senders", visible: (s, id) => orgAdmin(s, id), Component: SendersTab },
  { value: "resources", label: () => "Rooms and equipment", visible: (s, id) => orgAdmin(s, id), Component: ResourcesTab },
  { value: "contacts", label: () => "Contacts", visible: (s, id) => orgAdmin(s, id), Component: ContactsTab },
  { value: "members", label: () => "Members", visible: (s, id) => hasPerm(s, id, "members:read"), Component: MembersTab },
  { value: "roles", label: () => "Roles", visible: (s, id) => hasPerm(s, id, "roles:read"), Component: RolesTab },
  { value: "forwarding", label: () => "Forwarding", visible: (s, id) => orgAdmin(s, id), Component: ForwardingTab },
  { value: "security", label: () => "Security", visible: (s, id) => orgAdmin(s, id), Component: SecurityTab },
  { value: "sending", label: () => "Sending", visible: (s, id) => orgAdmin(s, id), Component: SendingTab },
  { value: "branding", label: () => "Branding", visible: (s, id) => hasPerm(s, id, "branding:read"), Component: BrandingTab },
  { value: "privacy", label: () => "Privacy", visible: (s, id) => orgAdmin(s, id), Component: PrivacyTab },
  { value: "signatures", label: () => "Signatures", visible: (s, id) => hasPerm(s, id, "signatures:read"), Component: SignaturesTab },
  { value: "quarantine", label: () => "Quarantine", visible: (s, id) => orgAdmin(s, id), Component: QuarantineTab },
  { value: "spam", label: () => "Spam filter", visible: (s, id) => orgAdmin(s, id), Component: SpamFilterTab },
  { value: "mail-flow", label: () => "Mail flow", visible: (s, id) => orgAdmin(s, id), Component: MailFlowTab },
  { value: "message-trace", label: () => "Message trace", visible: (s, id) => orgAdmin(s, id), Component: MessageTraceTab },
  { value: "retention", label: () => "Retention", visible: (s, id) => orgAdmin(s, id), Component: RetentionTab },
  { value: "sender-warnings", label: () => "Sender warnings", visible: (s, id) => orgAdmin(s, id), Component: SenderWarningsTab },
  { value: "attachments", label: () => "Attachments", visible: (s, id) => orgAdmin(s, id), Component: AttachmentsTab },
  { value: "encryption", label: () => "Encryption", visible: (s, id) => orgAdmin(s, id), Component: EncryptionTab },
  { value: "limits", label: () => "Limits", visible: (s, id) => orgAdmin(s, id), Component: LimitsTab },
  { value: "audit", label: () => "Audit log", visible: (s, id) => hasPerm(s, id, "audit:read"), Component: AuditTab },
  { value: "integrations", label: () => "Integrations", visible: (s, id) => hasPerm(s, id, "api_keys:read", "webhooks:read"), Component: IntegrationsTab },
]

export function AdminView() {
  const qc = useQueryClient()
  const session = useSession().data
  const orgs = useQuery(organizationsQuery)
  const [orgId, setOrgId] = useState<string | null>(null)
  const [tab, setTab] = useState("domains")
  const [creating, setCreating] = useState(false)
  const form = useForm<OrgValues>({ resolver: zodResolver(orgSchema), defaultValues: { name: "", slug: "" } })

  const create = useMutation({
    mutationFn: (values: OrgValues) => api<Organization>("/organizations", { method: "POST", body: values }),
    onSuccess: (org) => {
      toast.success(`Created ${org.name}`)
      qc.invalidateQueries({ queryKey: ["organizations"] })
      qc.invalidateQueries({ queryKey: ["session"] })
      setOrgId(org.id)
      setCreating(false)
      form.reset()
    },
  })

  if (!session) return null
  const platformAdmin = session.user.isPlatformAdmin
  const administered = (orgs.data ?? []).filter(
    (o) => platformAdmin || session.memberships.some((m) => m.orgId === o.id && (m.permissions.length > 0 || m.role !== "member"))
  )
  const org = administered.find((o) => o.id === orgId) ?? administered[0]
  const membership = org ? orgMembership(session, org.id) : undefined
  const tabs = org ? orgTabs.filter((t) => t.visible(session, org.id)) : []
  const active = platformAdmin && ["identity", "mail-queue", "routing", "blocked"].includes(tab) ? tab : tabs.some((t) => t.value === tab) ? tab : tabs[0]?.value ?? "identity"
  const scoped = org ? isDomainScoped(session, org.id) : false
  const showRoleBadge = !platformAdmin && membership && membership.role !== "owner" && membership.role !== "admin"

  return (
    <PageContainer>
      <PageHeader
        title="Administration"
        description={
          showRoleBadge ? (
            <span className="inline-flex flex-wrap items-center gap-2">
              <StatusBadge tone={roleBadgeTone(membership.role)}>{roleLabel(membership.role, membership.customRoleName)}</StatusBadge>
              {scoped && (
                <span>
                  {membership.domainIds.length === 0
                    ? "No domains assigned yet."
                    : `Scoped to ${plural(membership.domainIds.length, "domain")}.`}
                </span>
              )}
              {membership.role === "auditor" && <span>Read-only access.</span>}
            </span>
          ) : (
            "Domains, mailboxes and access for the organizations you manage."
          )
        }
        actions={
          <>
            {administered.length > 0 && org && (
              <Select value={org.id} onValueChange={setOrgId}>
                <SelectTrigger aria-label="Organization" className="min-w-56">
                  <Building2 aria-hidden className="text-fg-tertiary" />
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {administered.map((o) => (
                    <SelectItem key={o.id} value={o.id}>
                      {o.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            {platformAdmin && (
              <Button variant="secondary" onClick={() => setCreating(true)}>
                <Plus aria-hidden />
                New organization
              </Button>
            )}
          </>
        }
      />
      {orgs.isPending ? (
        <RowsSkeleton rows={4} />
      ) : orgs.isError ? (
        <ErrorState error={orgs.error} onRetry={() => orgs.refetch()} />
      ) : !org && !platformAdmin ? (
        <EmptyState icon={Building2} title="Nothing to administer" description="You do not have an administrative role in any organization." />
      ) : (
        <>
          {!org && (
            <EmptyState
              icon={Building2}
              title="No organizations yet"
              description="Create an organization to add domains and mailboxes."
              action={<Button onClick={() => setCreating(true)}>Create organization</Button>}
            />
          )}
        {(tabs.length > 0 || platformAdmin) && (
          <Tabs value={active} onValueChange={setTab}>
            <TabsList>
              {tabs.map((t) => (
                <TabsTrigger key={t.value} value={t.value}>
                  {org ? t.label(session, org.id) : t.value}
                </TabsTrigger>
              ))}
              {platformAdmin && <TabsTrigger value="identity">Identity providers</TabsTrigger>}
              {platformAdmin && <TabsTrigger value="mail-queue">Mail queue</TabsTrigger>}
              {platformAdmin && <TabsTrigger value="routing">Outbound routing</TabsTrigger>}
              {platformAdmin && <TabsTrigger value="blocked">Blocked addresses</TabsTrigger>}
            </TabsList>
            {org &&
              tabs.map(({ value, Component }) => (
                <TabsContent key={value} value={value}>
                  <Component key={org.id} orgId={org.id} />
                </TabsContent>
              ))}
            {platformAdmin && (
              <TabsContent value="routing"><OutboundRoutingTab /></TabsContent>
            )}
            {platformAdmin && (
              <TabsContent value="identity">
                <IdentityProvidersTab organizations={orgs.data} />
              </TabsContent>
            )}
            {platformAdmin && (
              <TabsContent value="mail-queue">
                <MailQueueTab />
              </TabsContent>
            )}
            {platformAdmin && (
              <TabsContent value="blocked">
                <BlockedAddressesTab />
              </TabsContent>
            )}
          </Tabs>
        )}
        </>
      )}

      <FormDialog
        open={creating}
        onOpenChange={(open) => {
          setCreating(open)
          if (!open) form.reset()
        }}
        title="New organization"
        description="Organizations own domains, mailboxes and members."
        submitLabel="Create organization"
        pending={create.isPending}
        onSubmit={form.handleSubmit((v) => create.mutate(v))}
      >
        <Field id="org-name" label="Name" error={form.formState.errors.name?.message}>
          <Input id="org-name" autoComplete="off" aria-invalid={!!form.formState.errors.name} {...form.register("name")} />
        </Field>
        <Field id="org-slug" label="Slug" hint="Used in URLs and identity provider mappings. Lowercase, for example “scala-studios”." error={form.formState.errors.slug?.message}>
          <Input id="org-slug" autoComplete="off" spellCheck={false} className="font-mono" aria-invalid={!!form.formState.errors.slug} {...form.register("slug")} />
        </Field>
      </FormDialog>
    </PageContainer>
  )
}

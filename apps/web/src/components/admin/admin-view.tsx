"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Building2, Plus } from "lucide-react"
import { useState } from "react"
import { useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod"

import { AliasesTab } from "@/components/admin/aliases-tab"
import { AuditTab } from "@/components/admin/audit-tab"
import { DomainsTab } from "@/components/admin/domains-tab"
import { IdentityProvidersTab } from "@/components/admin/identity-providers-tab"
import { IntegrationsTab } from "@/components/admin/integrations-tab"
import { MailboxesTab } from "@/components/admin/mailboxes-tab"
import { MembersTab } from "@/components/admin/members-tab"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { PageContainer } from "@/components/page-container"
import { PageHeader } from "@/components/page-header"
import { EmptyState, ErrorState, RowsSkeleton } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useSession } from "@/hooks/use-session"
import { api } from "@/lib/api"
import { organizationsQuery } from "@/lib/queries"
import type { Organization } from "@/lib/types"

const orgSchema = z.object({
  name: z.string().trim().min(1, "Enter the organization name."),
  slug: z.string().regex(/^[a-z0-9][a-z0-9-]{0,62}$/, "Use lowercase letters, digits and hyphens, starting with a letter or digit."),
})

type OrgValues = z.infer<typeof orgSchema>

const orgTabs = [
  { value: "domains", label: "Domains", Component: DomainsTab },
  { value: "mailboxes", label: "Mailboxes", Component: MailboxesTab },
  { value: "aliases", label: "Aliases", Component: AliasesTab },
  { value: "members", label: "Members", Component: MembersTab },
  { value: "audit", label: "Audit log", Component: AuditTab },
  { value: "integrations", label: "Integrations", Component: IntegrationsTab },
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
    (o) => platformAdmin || session.memberships.some((m) => m.orgId === o.id && m.role !== "member")
  )
  const org = administered.find((o) => o.id === orgId) ?? administered[0]
  const tabs = org ? orgTabs : []
  const active = tab === "identity" && platformAdmin ? tab : tabs.some((t) => t.value === tab) ? tab : tabs[0]?.value ?? "identity"

  return (
    <PageContainer>
      <PageHeader
        title="Administration"
        description="Domains, mailboxes and access for the organizations you manage."
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
        <EmptyState icon={Building2} title="Nothing to administer" description="You are not an owner or administrator of any organization." />
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
        <Tabs value={active} onValueChange={setTab}>
          <TabsList>
            {tabs.map((t) => (
              <TabsTrigger key={t.value} value={t.value}>
                {t.label}
              </TabsTrigger>
            ))}
            {platformAdmin && <TabsTrigger value="identity">Identity providers</TabsTrigger>}
          </TabsList>
          {org &&
            tabs.map(({ value, Component }) => (
              <TabsContent key={value} value={value}>
                <Component key={org.id} orgId={org.id} />
              </TabsContent>
            ))}
          {platformAdmin && (
            <TabsContent value="identity">
              <IdentityProvidersTab organizations={orgs.data} />
            </TabsContent>
          )}
        </Tabs>
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

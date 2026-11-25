"use client"

import { Building2, Gavel } from "lucide-react"
import { useState } from "react"

import { CaseDetail } from "@/components/compliance/case-detail"
import { CasesTab } from "@/components/compliance/cases-tab"
import { JournalTab } from "@/components/compliance/journal-tab"
import { PageContainer } from "@/components/page-container"
import { PageHeader } from "@/components/page-header"
import { EmptyState, RowsSkeleton } from "@/components/states"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useSession } from "@/hooks/use-session"

export function ComplianceView() {
  const session = useSession()
  const [orgId, setOrgId] = useState<string | null>(null)
  const [caseId, setCaseId] = useState<string | null>(null)
  const orgs = session.data?.memberships.filter((m) => m.compliance) ?? []
  const org = orgs.find((o) => o.orgId === orgId) ?? orgs[0]

  return (
    <PageContainer>
      <PageHeader
        title="Compliance"
        description="Keep mail under legal hold, search across mailboxes, export what you find and journal mail to an archive. Every action is recorded in the audit log."
        actions={
          orgs.length > 1 &&
          org && (
            <Select
              value={org.orgId}
              onValueChange={(id) => {
                setOrgId(id)
                setCaseId(null)
              }}
            >
              <SelectTrigger aria-label="Organization" className="min-w-56">
                <Building2 aria-hidden className="text-fg-tertiary" />
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {orgs.map((o) => (
                  <SelectItem key={o.orgId} value={o.orgId}>
                    {o.orgName}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )
        }
      />
      {session.isPending ? (
        <RowsSkeleton rows={4} />
      ) : !org ? (
        <EmptyState icon={Gavel} title="No compliance access" description="An owner of your organization can make you a compliance administrator under Administration → Members." />
      ) : caseId ? (
        <CaseDetail key={caseId} caseId={caseId} orgId={org.orgId} onBack={() => setCaseId(null)} />
      ) : (
        <Tabs defaultValue="cases">
          <TabsList>
            <TabsTrigger value="cases">Cases</TabsTrigger>
            <TabsTrigger value="journaling">Journaling</TabsTrigger>
          </TabsList>
          <TabsContent value="cases">
            <CasesTab key={org.orgId} orgId={org.orgId} onOpen={setCaseId} />
          </TabsContent>
          <TabsContent value="journaling">
            <JournalTab key={org.orgId} orgId={org.orgId} />
          </TabsContent>
        </Tabs>
      )}
    </PageContainer>
  )
}

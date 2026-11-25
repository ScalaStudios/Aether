import { queryOptions } from "@tanstack/react-query"

import { api } from "@/lib/api"
import type { AlertList, AlertRule, Branding, CalendarInfo, ComplianceCase, ComplianceDirectory, Domain, Folder, Hold, JournalRule, Label, Mailbox, Member, Organization, PersonalAliasPolicy, SavedSearch, OrgRole, OrganizationLimits, QuotaDefault, OrgFeatures, SignatureSettings } from "@/lib/types"

export const myMailboxesQuery = queryOptions({
  queryKey: ["my-mailboxes"],
  queryFn: () => api<Mailbox[]>("/me/mailboxes"),
})

export const organizationsQuery = queryOptions({
  queryKey: ["organizations"],
  queryFn: () => api<Organization[]>("/organizations"),
})

export const limitsQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["limits", orgId],
    queryFn: () => api<OrganizationLimits>(`/organizations/${orgId}/limits`),
  })

export const organizationDefaultsQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["defaults", orgId],
    queryFn: () => api<QuotaDefault>(`/organizations/${orgId}/defaults`),
  })

export const domainsQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["domains", orgId],
    queryFn: () => api<Domain[]>(`/organizations/${orgId}/domains`),
  })

export const mailboxesQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["mailboxes", orgId],
    queryFn: () => api<Mailbox[]>(`/organizations/${orgId}/mailboxes`),
  })

export const domainDefaultsQuery = (domainId: string) =>
  queryOptions({
    queryKey: ["domain-defaults", domainId],
    queryFn: () => api<QuotaDefault>(`/domains/${domainId}/defaults`),
  })

export const membersQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["members", orgId],
    queryFn: () => api<Member[]>(`/organizations/${orgId}/members`),
  })

export const rolesQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["roles", orgId],
    queryFn: () => api<OrgRole[]>(`/organizations/${orgId}/roles`),
  })

export const featuresQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["features", orgId],
    queryFn: () => api<OrgFeatures>(`/organizations/${orgId}/features`),
  })

export const foldersQuery = (mailboxId: string) =>
  queryOptions({
    queryKey: ["folders", mailboxId],
    queryFn: () => api<Folder[]>(`/mailboxes/${mailboxId}/folders`),
  })

export const labelsQuery = (mailboxId: string) =>
  queryOptions({
    queryKey: ["labels", mailboxId],
    queryFn: () => api<Label[]>(`/mailboxes/${mailboxId}/labels`),
  })

export const savedSearchesQuery = (mailboxId: string) =>
  queryOptions({
    queryKey: ["saved-searches", mailboxId],
    queryFn: () => api<SavedSearch[]>(`/mailboxes/${mailboxId}/saved-searches`),
  })

export const signaturesQuery = (mailboxId: string, fromAddress?: string) =>
  queryOptions({
    queryKey: ["signatures", mailboxId, fromAddress],
    queryFn: () => api<SignatureSettings>(`/mailboxes/${mailboxId}/signatures`, { query: { fromAddress } }),
  })

export const calendarsQuery = queryOptions({
  queryKey: ["calendars"],
  queryFn: () => api<CalendarInfo[]>("/calendar/calendars"),
})

export const brandingQuery = queryOptions({
  queryKey: ["branding"],
  queryFn: () => api<Branding | null>("/branding", { query: { host: window.location.hostname } }),
})

export const casesQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["compliance-cases", orgId],
    queryFn: () => api<ComplianceCase[]>(`/organizations/${orgId}/compliance/cases`),
  })

export const caseQuery = (caseId: string) =>
  queryOptions({
    queryKey: ["compliance-case", caseId],
    queryFn: () => api<ComplianceCase>(`/compliance/cases/${caseId}`),
  })

export const holdsQuery = (caseId: string) =>
  queryOptions({
    queryKey: ["compliance-holds", caseId],
    queryFn: () => api<Hold[]>(`/compliance/cases/${caseId}/holds`),
  })

export const complianceDirectoryQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["compliance-directory", orgId],
    queryFn: () => api<ComplianceDirectory>(`/organizations/${orgId}/compliance/mailboxes`),
  })

export const journalRulesQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["journal-rules", orgId],
    queryFn: () => api<JournalRule[]>(`/organizations/${orgId}/journal-rules`),
  })

export const personalAliasPolicyQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["personal-alias-policy", orgId],
    queryFn: () => api<PersonalAliasPolicy>(`/organizations/${orgId}/personal-alias-policy`),
  })

export const alertSummaryQuery = queryOptions({
  queryKey: ["alerts", "summary"],
  queryFn: () => api<AlertList>("/admin/alerts", { query: { status: "firing", limit: 1 } }),
  select: (data) => data.summary,
  refetchInterval: 60_000,
})

export const alertRulesQuery = queryOptions({
  queryKey: ["alert-rules"],
  queryFn: () => api<AlertRule[]>("/admin/alerts/rules"),
})

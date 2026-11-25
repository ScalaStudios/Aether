import { queryOptions } from "@tanstack/react-query"

import { api } from "@/lib/api"
import type { Branding, CalendarInfo, ComplianceCase, ComplianceDirectory, Domain, Folder, Hold, JournalRule, Mailbox, Member, Organization, SignatureSettings } from "@/lib/types"

export const myMailboxesQuery = queryOptions({
  queryKey: ["my-mailboxes"],
  queryFn: () => api<Mailbox[]>("/me/mailboxes"),
})

export const organizationsQuery = queryOptions({
  queryKey: ["organizations"],
  queryFn: () => api<Organization[]>("/organizations"),
})

export const domainsQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["domains", orgId],
    queryFn: () => api<Domain[]>(`/organizations/${orgId}/domains`),
  })

export const membersQuery = (orgId: string) =>
  queryOptions({
    queryKey: ["members", orgId],
    queryFn: () => api<Member[]>(`/organizations/${orgId}/members`),
  })

export const foldersQuery = (mailboxId: string) =>
  queryOptions({
    queryKey: ["folders", mailboxId],
    queryFn: () => api<Folder[]>(`/mailboxes/${mailboxId}/folders`),
  })

export const signaturesQuery = (mailboxId: string) =>
  queryOptions({
    queryKey: ["signatures", mailboxId],
    queryFn: () => api<SignatureSettings>(`/mailboxes/${mailboxId}/signatures`),
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

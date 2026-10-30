import { queryOptions } from "@tanstack/react-query"

import { api } from "@/lib/api"
import type { Branding, CalendarInfo, Domain, Folder, Mailbox, Member, Organization, SignatureSettings } from "@/lib/types"

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

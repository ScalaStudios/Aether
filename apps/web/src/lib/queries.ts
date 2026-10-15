import { queryOptions } from "@tanstack/react-query"

import { api } from "@/lib/api"
import type { CalendarInfo, Domain, Folder, Mailbox, Organization } from "@/lib/types"

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

export const foldersQuery = (mailboxId: string) =>
  queryOptions({
    queryKey: ["folders", mailboxId],
    queryFn: () => api<Folder[]>(`/mailboxes/${mailboxId}/folders`),
  })

export const calendarsQuery = queryOptions({
  queryKey: ["calendars"],
  queryFn: () => api<CalendarInfo[]>("/calendar/calendars"),
})

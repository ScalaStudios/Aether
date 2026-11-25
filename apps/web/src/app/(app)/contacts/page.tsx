import type { Metadata } from "next"

import { ContactsView } from "@/components/contacts/contacts-view"

export const metadata: Metadata = {
  title: "Contacts",
  description: "Address books you can share, groups you can write to at once, and the directory of everyone in your organizations.",
}

export default function ContactsPage() {
  return <ContactsView />
}

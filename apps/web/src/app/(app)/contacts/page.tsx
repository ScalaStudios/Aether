import type { Metadata } from "next"

import { ContactsView } from "@/components/contacts/contacts-view"

export const metadata: Metadata = {
  title: "Contacts",
  description: "Your Aether address book, synced to phones over CardDAV, and the directory of everyone in your organizations.",
}

export default function ContactsPage() {
  return <ContactsView />
}

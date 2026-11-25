import type { Tone } from "@/components/status-badge"
import type { QuarantineAccess, QuarantineCategory, QuarantineDigest, QuarantineSymbol } from "@/lib/types"

export const quarantineCategories: Record<QuarantineCategory, { label: string; tone: Tone; reason: string; hint: string }> = {
  spam: {
    label: "Spam",
    tone: "warning",
    reason: "It looks like unsolicited bulk mail.",
    hint: "Scored 10 or more by the spam filter. Below 10, mail goes to Junk instead.",
  },
  high_confidence_spam: {
    label: "High-confidence spam",
    tone: "warning",
    reason: "It scored high enough that it would otherwise have been rejected.",
    hint: "Scored 15 or more, or matched a rule that rejects mail outright.",
  },
  phishing: {
    label: "Phishing",
    tone: "danger",
    reason: "It looks like an attempt to collect a password, payment or personal details.",
    hint: "Links that disguise where they lead, or addresses on phishing lists.",
  },
  malware: {
    label: "Malware",
    tone: "danger",
    reason: "The virus scanner found malicious content in it.",
    hint: "Attachments or content that ClamAV flagged.",
  },
}

export const categoryOrder = Object.keys(quarantineCategories) as QuarantineCategory[]

export const accessOptions: { value: QuarantineAccess; label: string }[] = [
  { value: "release", label: "People release it" },
  { value: "request", label: "People request release" },
  { value: "none", label: "Administrators only" },
]

export const digestOptions: { value: QuarantineDigest; label: string }[] = [
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
  { value: "off", label: "Off" },
]

export function virusName(symbols: QuarantineSymbol[]) {
  return symbols.find((s) => s.name === "CLAM_VIRUS")?.options?.[0]
}

export function risky(category: QuarantineCategory) {
  return category === "phishing" || category === "malware"
}

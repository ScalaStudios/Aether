import type { AlertCategory, AlertSeverity, AlertStatus } from "@/lib/types"

export const alertSeverities: Record<AlertSeverity, { tone: "danger" | "warning" | "info"; label: string }> = {
  critical: { tone: "danger", label: "Critical" },
  warning: { tone: "warning", label: "Warning" },
  info: { tone: "info", label: "Info" },
}

export const alertStatuses: Record<AlertStatus, { tone: "danger" | "info" | "success"; label: string }> = {
  firing: { tone: "danger", label: "Firing" },
  acknowledged: { tone: "info", label: "Acknowledged" },
  resolved: { tone: "success", label: "Resolved" },
}

export const alertCategories: Record<AlertCategory, string> = {
  mail: "Mail flow",
  security: "Security",
  dns: "Domains",
  dovecot: "Mailbox access",
  storage: "Storage",
}

export const alertDocsUrl = "https://github.com/ScalaStudios/Aether/blob/main/documentation/installation.md#alerts"

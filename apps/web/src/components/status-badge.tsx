import { CircleAlert, CircleCheck, CircleMinus, CircleX, Info, type LucideIcon } from "lucide-react"
import { cn } from "cn"

type Tone = "success" | "warning" | "danger" | "info" | "neutral"

const tones: Record<Tone, { icon: LucideIcon; className: string }> = {
  success: { icon: CircleCheck, className: "text-success bg-success/12" },
  warning: { icon: CircleAlert, className: "text-warning bg-warning/12" },
  danger: { icon: CircleX, className: "text-danger bg-danger/12" },
  info: { icon: Info, className: "text-info bg-info/12" },
  neutral: { icon: CircleMinus, className: "text-fg-secondary bg-muted" },
}

export function StatusBadge({ tone, children }: { tone: Tone; children: React.ReactNode }) {
  const { icon: Icon, className } = tones[tone]
  return (
    <span className={cn("inline-flex w-fit items-center gap-1 rounded-full px-2 py-1 text-xs font-semibold whitespace-nowrap", className)}>
      <Icon aria-hidden className="size-3.5" />
      {children}
    </span>
  )
}

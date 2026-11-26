import { CircleAlert, CircleX } from "lucide-react"
import { cn } from "@/lib/utils"

const tones = {
  warning: { icon: CircleAlert, role: "status", box: "border-warning/40 bg-warning/12", text: "text-warning" },
  danger: { icon: CircleX, role: "alert", box: "border-danger/40 bg-danger/12", text: "text-danger" },
}

export function Notice({ tone, children }: { tone: keyof typeof tones; children: React.ReactNode }) {
  const { icon: Icon, role, box, text } = tones[tone]
  return (
    <div role={role} className={cn("flex gap-3 rounded-lg border p-4 text-sm", box)}>
      <Icon aria-hidden className={cn("size-4 shrink-0 translate-y-0.5", text)} />
      <span>{children}</span>
    </div>
  )
}

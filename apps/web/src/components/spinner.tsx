import { Loader2 } from "lucide-react"
import { cn } from "cn"

export function Spinner({ className }: { className?: string }) {
  return <Loader2 aria-hidden className={cn("size-4 animate-spin text-ember", className)} />
}

import { Search } from "lucide-react"
import { cn } from "cn"

import { Input } from "@/components/ui/input"

export function SearchInput({ className, ...props }: React.ComponentProps<typeof Input>) {
  return (
    <div className={cn("relative", className)}>
      <Search aria-hidden className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-tertiary" />
      <Input type="search" onKeyDown={(e) => e.key === "Escape" && e.currentTarget.blur()} className="pl-10" {...props} />
    </div>
  )
}

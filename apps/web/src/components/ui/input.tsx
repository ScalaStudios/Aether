import * as React from "react"
import { cn } from "cn"

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        "h-10 w-full min-w-0 rounded-lg border border-input bg-sunken px-4 py-2 text-base text-foreground transition-colors duration-160 outline-none placeholder:text-fg-tertiary focus-visible:border-ember disabled:cursor-not-allowed disabled:opacity-45 aria-invalid:border-danger max-md:h-11",
        className
      )}
      {...props}
    />
  )
}

export { Input }

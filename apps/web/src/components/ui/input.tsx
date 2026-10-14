import * as React from "react"
import { cn } from "cn"

const field =
  "w-full min-w-0 rounded-lg border border-input bg-sunken px-4 py-2 text-base text-foreground transition-colors duration-160 outline-none placeholder:text-fg-tertiary focus-visible:border-ember disabled:cursor-not-allowed disabled:opacity-45 aria-invalid:border-danger"

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return <input type={type} data-slot="input" className={cn(field, "h-10 max-md:h-11", className)} {...props} />
}

function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return <textarea data-slot="textarea" className={cn(field, "min-h-32 py-3", className)} {...props} />
}

export { Input, Textarea }

"use client"

import * as React from "react"
import { CheckIcon, MinusIcon } from "lucide-react"
import { cn } from "cn"
import { Checkbox as CheckboxPrimitive } from "radix-ui"

function Checkbox({ className, ...props }: React.ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      data-slot="checkbox"
      className={cn(
        "peer relative flex size-4 shrink-0 cursor-pointer items-center justify-center rounded-sm border border-strong text-ink transition-colors duration-160 ease-brand outline-none after:absolute after:-inset-3 hover:border-fg-tertiary disabled:cursor-not-allowed disabled:opacity-45 data-[state=checked]:border-ember data-[state=checked]:bg-ember data-[state=indeterminate]:border-ember data-[state=indeterminate]:bg-ember",
        className
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator data-slot="checkbox-indicator" className="flex items-center justify-center">
        {props.checked === "indeterminate" ? <MinusIcon className="size-3.5" /> : <CheckIcon className="size-3.5" />}
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  )
}

export { Checkbox }

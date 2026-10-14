import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "cn"
import { Slot } from "radix-ui"

import { Spinner } from "@/components/spinner"

const buttonVariants = cva(
  "relative inline-flex shrink-0 cursor-pointer items-center justify-center gap-2 rounded-lg border border-transparent font-semibold whitespace-nowrap transition-[background-color,border-color,color,box-shadow] duration-160 ease-brand outline-none select-none disabled:pointer-events-none disabled:opacity-45 aria-disabled:pointer-events-none [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        default: "bg-ember text-ink hover:bg-ember-hover hover:shadow-ember active:bg-ember-active",
        secondary:
          "border-strong bg-transparent text-foreground hover:border-fg-tertiary hover:bg-foreground/4 active:bg-foreground/8",
        ghost: "bg-ember/12 text-primary hover:bg-ember/20 active:bg-ember/28",
        quiet: "bg-transparent text-fg-secondary hover:bg-muted hover:text-foreground aria-expanded:bg-muted",
        destructive: "bg-danger text-white hover:bg-[#c92438] active:bg-[#b01e30]",
        link: "h-auto! p-0! font-medium text-link underline underline-offset-3 hover:text-ember",
      },
      size: {
        default: "min-h-10 px-6 py-3 text-[15px] leading-[1.2]",
        sm: "min-h-8 px-4 py-2 text-[13px] leading-[1.2]",
        lg: "min-h-12 px-8 py-4 text-[15px] leading-[1.2]",
        icon: "size-10 max-md:size-11",
        "icon-sm": "size-8 max-md:size-11",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  loading = false,
  disabled,
  children,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean
    loading?: boolean
  }) {
  const Comp = asChild ? Slot.Root : "button"

  return (
    <Comp
      data-slot="button"
      data-variant={variant}
      data-size={size}
      className={cn(buttonVariants({ variant, size, className }))}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...props}
    >
      {asChild ? (
        children
      ) : loading ? (
        <>
          <span className="invisible inline-flex items-center gap-2">{children}</span>
          <span className="absolute inset-0 flex items-center justify-center">
            <Spinner className={variant === "default" ? "text-ink" : undefined} />
          </span>
        </>
      ) : (
        children
      )}
    </Comp>
  )
}

export { Button, buttonVariants }

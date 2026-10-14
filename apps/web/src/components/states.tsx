"use client"

import type { UseQueryResult } from "@tanstack/react-query"
import { TriangleAlert, type LucideIcon } from "lucide-react"
import { cn } from "cn"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { errorMessage } from "@/lib/api"

export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  className,
}: {
  icon: LucideIcon
  title: string
  description?: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("animate-content flex flex-col items-center justify-center gap-3 px-6 py-12 text-center", className)}>
      <Icon aria-hidden className="size-8 text-fg-tertiary" />
      <h2 className="text-h3">{title}</h2>
      {description && <p className="max-w-[48ch] text-fg-secondary">{description}</p>}
      {action && <div className="pt-3">{action}</div>}
    </div>
  )
}

export function ErrorState({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  return (
    <EmptyState
      icon={TriangleAlert}
      title="This could not be loaded"
      description={errorMessage(error)}
      className={className}
      action={
        onRetry && (
          <Button variant="secondary" onClick={onRetry}>
            Try again
          </Button>
        )
      }
    />
  )
}

export function RowsSkeleton({ rows = 3, className }: { rows?: number; className?: string }) {
  return (
    <div aria-busy aria-label="Loading" className={cn("flex flex-col gap-3", className)}>
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-12 w-full" />
      ))}
    </div>
  )
}

export function QueryView<T>({
  query,
  empty,
  isEmpty = (data) => Array.isArray(data) && data.length === 0,
  skeleton = <RowsSkeleton />,
  children,
}: {
  query: UseQueryResult<T>
  empty?: React.ReactNode
  isEmpty?: (data: T) => boolean
  skeleton?: React.ReactNode
  children: (data: T) => React.ReactNode
}) {
  if (query.isPending) return skeleton
  if (query.isError) return <ErrorState error={query.error} onRetry={() => query.refetch()} />
  if (empty && isEmpty(query.data)) return empty
  return <div className="animate-content">{children(query.data)}</div>
}

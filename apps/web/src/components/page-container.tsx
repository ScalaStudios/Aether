export function PageContainer({ children }: { children: React.ReactNode }) {
  return (
    <div className="h-full overflow-y-auto">
      <div className="animate-content mx-auto flex w-full max-w-[1440px] flex-col gap-12 px-5 py-8 md:px-8 md:py-12 lg:px-12">{children}</div>
    </div>
  )
}

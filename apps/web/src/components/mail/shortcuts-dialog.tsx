"use client"

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"

const shortcuts: [string[], string][] = [
  [["c"], "New message"],
  [["j"], "Next message"],
  [["k"], "Previous message"],
  [["r"], "Reply"],
  [["a"], "Reply all"],
  [["f"], "Forward"],
  [["e", "#"], "Delete"],
  [["/"], "Search"],
  [["Esc"], "Close the open message or composer"],
  [["?"], "Show this list"],
]

export function ShortcutsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>Shortcuts work in Mail while you are not typing in a field.</DialogDescription>
        </DialogHeader>
        <dl className="flex flex-col">
          {shortcuts.map(([keys, label]) => (
            <div key={label} className="flex items-center justify-between gap-4 border-b border-border py-2 last:border-b-0">
              <dt className="text-fg-secondary">{label}</dt>
              <dd className="flex gap-1">
                {keys.map((key) => (
                  <kbd key={key} className="min-w-8 rounded-sm border border-strong bg-sunken px-2 py-1 text-center font-mono text-xs">
                    {key}
                  </kbd>
                ))}
              </dd>
            </div>
          ))}
        </dl>
      </DialogContent>
    </Dialog>
  )
}

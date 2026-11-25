"use client"

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useI18n } from "@/lib/i18n"

const shortcuts: [string[], string][] = [
  [["c"], "mail.shortcuts.newMessage"],
  [["j"], "mail.shortcuts.nextMessage"],
  [["k"], "mail.shortcuts.previousMessage"],
  [["r"], "mail.shortcuts.reply"],
  [["a"], "mail.shortcuts.replyAll"],
  [["f"], "mail.shortcuts.forward"],
  [["e", "#"], "mail.shortcuts.delete"],
  [["/"], "mail.shortcuts.search"],
  [["Esc"], "mail.shortcuts.closeOpen"],
  [["Ctrl+Z", "⌘Z"], "Undo the message you just sent"],
  [["?"], "mail.shortcuts.showList"],
]

export function ShortcutsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useI18n()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("mail.shortcuts.title")}</DialogTitle>
          <DialogDescription>{t("mail.shortcuts.description")}</DialogDescription>
        </DialogHeader>
        <dl className="flex flex-col">
          {shortcuts.map(([keys, labelKey]) => (
            <div key={labelKey} className="flex items-center justify-between gap-4 border-b border-border py-2 last:border-b-0">
              <dt className="text-fg-secondary">{t(labelKey)}</dt>
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

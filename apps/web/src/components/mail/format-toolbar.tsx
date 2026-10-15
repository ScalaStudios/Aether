"use client"

import SubscriptMark from "@tiptap/extension-subscript"
import SuperscriptMark from "@tiptap/extension-superscript"
import TextAlign from "@tiptap/extension-text-align"
import { TextStyleKit } from "@tiptap/extension-text-style"
import { useEditorState, type Editor } from "@tiptap/react"
import StarterKit from "@tiptap/starter-kit"
import {
  AlignCenter,
  AlignLeft,
  AlignRight,
  Baseline,
  Bold,
  Highlighter,
  IndentDecrease,
  IndentIncrease,
  Italic,
  Link2,
  List,
  ListOrdered,
  Redo2,
  Signature as SignatureIcon,
  RemoveFormatting,
  Strikethrough,
  Subscript,
  Superscript,
  TextQuote,
  Underline,
  Undo2,
  type LucideIcon,
} from "lucide-react"
import { cn } from "cn"

import { IconButton } from "@/components/icon-button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import type { Signature } from "@/lib/types"

export const mailEditorExtensions = [
  StarterKit.configure({ heading: false, link: { openOnClick: false, defaultProtocol: "https" } }),
  TextStyleKit.configure({ lineHeight: false }),
  TextAlign.configure({ types: ["paragraph"] }),
  SubscriptMark,
  SuperscriptMark,
]

const fonts = [
  { label: "Default font", value: "default" },
  { label: "Arial", value: "Arial, Helvetica, sans-serif" },
  { label: "Verdana", value: "Verdana, Geneva, sans-serif" },
  { label: "Trebuchet MS", value: "'Trebuchet MS', sans-serif" },
  { label: "Georgia", value: "Georgia, serif" },
  { label: "Times New Roman", value: "'Times New Roman', Times, serif" },
  { label: "Courier New", value: "'Courier New', Courier, monospace" },
]

const sizes = ["default", "12px", "14px", "16px", "18px", "24px", "32px"]

const textColors = ["#1f2937", "#6b7280", "#dc2626", "#ea580c", "#ca8a04", "#16a34a", "#2563eb", "#7c3aed"]
const highlights = ["#fef08a", "#fed7aa", "#fbcfe8", "#bbf7d0", "#bfdbfe", "#e5e7eb"]

const alignments: { value: "left" | "center" | "right"; label: string; icon: LucideIcon }[] = [
  { value: "left", label: "Align left", icon: AlignLeft },
  { value: "center", label: "Center", icon: AlignCenter },
  { value: "right", label: "Align right", icon: AlignRight },
]

function Divider() {
  return <span aria-hidden className="mx-1 h-5 w-px self-center bg-border" />
}

function ColorMenu({ label, icon: Icon, current, colors, onPick, onClear, clearLabel }: { label: string; icon: LucideIcon; current?: string; colors: string[]; onPick: (c: string) => void; onClear: () => void; clearLabel: string }) {
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger aria-label={label} className="flex size-8 cursor-pointer flex-col items-center justify-center gap-0.5 rounded-lg text-fg-secondary transition-colors duration-160 hover:bg-muted hover:text-foreground aria-expanded:bg-muted max-md:size-11">
            <Icon className="size-4" />
            <span aria-hidden className="h-0.5 w-4 rounded-full" style={{ background: current ?? "currentColor" }} />
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent className="w-auto">
        <div className="grid grid-cols-4 gap-1 p-1">
          {colors.map((c) => (
            <DropdownMenuItem key={c} aria-label={c} onSelect={() => onPick(c)} className="size-8 justify-center p-0">
              <span className={cn("size-5 rounded-full border border-border", current === c && "ring-2 ring-ring")} style={{ background: c }} />
            </DropdownMenuItem>
          ))}
        </div>
        <DropdownMenuItem onSelect={onClear}>{clearLabel}</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export function FormatToolbar({ editor, signatures }: { editor: Editor; signatures?: Signature[] }) {
  const state = useEditorState({
    editor,
    selector: ({ editor }) => ({
      bold: editor.isActive("bold"),
      italic: editor.isActive("italic"),
      underline: editor.isActive("underline"),
      strike: editor.isActive("strike"),
      subscript: editor.isActive("subscript"),
      superscript: editor.isActive("superscript"),
      bulletList: editor.isActive("bulletList"),
      orderedList: editor.isActive("orderedList"),
      blockquote: editor.isActive("blockquote"),
      link: editor.isActive("link"),
      font: (editor.getAttributes("textStyle").fontFamily as string | undefined) ?? "default",
      size: (editor.getAttributes("textStyle").fontSize as string | undefined) ?? "default",
      color: editor.getAttributes("textStyle").color as string | undefined,
      highlight: editor.getAttributes("textStyle").backgroundColor as string | undefined,
      align: alignments.find((a) => editor.isActive({ textAlign: a.value }))?.value ?? "left",
      canUndo: editor.can().undo(),
      canRedo: editor.can().redo(),
      canIndent: editor.can().sinkListItem("listItem"),
      canOutdent: editor.can().liftListItem("listItem"),
    }),
  })
  const chain = () => editor.chain().focus()

  function setLink() {
    const previous = editor.getAttributes("link").href as string | undefined
    const url = window.prompt("Link address", previous ?? "https://")
    if (url === null) return
    if (url.trim() === "") chain().extendMarkRange("link").unsetLink().run()
    else chain().extendMarkRange("link").setLink({ href: url.trim() }).run()
  }

  const groups: { label: string; icon: LucideIcon; active?: boolean; disabled?: boolean; run: () => void }[][] = [
    [
      { label: "Undo", icon: Undo2, disabled: !state.canUndo, run: () => chain().undo().run() },
      { label: "Redo", icon: Redo2, disabled: !state.canRedo, run: () => chain().redo().run() },
    ],
    [
      { label: "Bold", icon: Bold, active: state.bold, run: () => chain().toggleBold().run() },
      { label: "Italic", icon: Italic, active: state.italic, run: () => chain().toggleItalic().run() },
      { label: "Underline", icon: Underline, active: state.underline, run: () => chain().toggleUnderline().run() },
      { label: "Strikethrough", icon: Strikethrough, active: state.strike, run: () => chain().toggleStrike().run() },
      { label: "Subscript", icon: Subscript, active: state.subscript, run: () => chain().unsetSuperscript().toggleSubscript().run() },
      { label: "Superscript", icon: Superscript, active: state.superscript, run: () => chain().unsetSubscript().toggleSuperscript().run() },
    ],
    [
      { label: "Bulleted list", icon: List, active: state.bulletList, run: () => chain().toggleBulletList().run() },
      { label: "Numbered list", icon: ListOrdered, active: state.orderedList, run: () => chain().toggleOrderedList().run() },
      { label: "Decrease indent", icon: IndentDecrease, disabled: !state.canOutdent, run: () => chain().liftListItem("listItem").run() },
      { label: "Increase indent", icon: IndentIncrease, disabled: !state.canIndent, run: () => chain().sinkListItem("listItem").run() },
      { label: "Quote", icon: TextQuote, active: state.blockquote, run: () => chain().toggleBlockquote().run() },
    ],
  ]
  const Align = alignments.find((a) => a.value === state.align)!.icon

  const button = (b: (typeof groups)[number][number]) => (
    <IconButton key={b.label} type="button" label={b.label} size="icon-sm" aria-pressed={b.active} disabled={b.disabled} onClick={b.run} className={b.active ? "bg-muted text-foreground" : undefined}>
      <b.icon />
    </IconButton>
  )

  return (
    <div role="toolbar" aria-label="Formatting" className="flex flex-wrap items-center gap-1 border-b border-border p-1">
      {groups[0].map(button)}
      <Divider />
      <Select value={state.font} onValueChange={(v) => (v === "default" ? chain().unsetFontFamily().run() : chain().setFontFamily(v).run())}>
        <SelectTrigger size="sm" aria-label="Font" className="w-36 pl-3 text-sm">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {fonts.map((f) => (
            <SelectItem key={f.value} value={f.value} style={f.value === "default" ? undefined : { fontFamily: f.value }}>
              {f.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select value={state.size} onValueChange={(v) => (v === "default" ? chain().unsetFontSize().run() : chain().setFontSize(v).run())}>
        <SelectTrigger size="sm" aria-label="Font size" className="w-24 pl-3 text-sm">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {sizes.map((s) => (
            <SelectItem key={s} value={s}>
              {s === "default" ? "Default" : s.replace("px", "")}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Divider />
      {groups[1].map(button)}
      <ColorMenu label="Text color" icon={Baseline} current={state.color} colors={textColors} clearLabel="Automatic" onPick={(c) => chain().setColor(c).run()} onClear={() => chain().unsetColor().run()} />
      <ColorMenu label="Highlight" icon={Highlighter} current={state.highlight} colors={highlights} clearLabel="No highlight" onPick={(c) => chain().setBackgroundColor(c).run()} onClear={() => chain().unsetBackgroundColor().run()} />
      <IconButton type="button" label="Clear formatting" size="icon-sm" onClick={() => chain().unsetAllMarks().clearNodes().run()}>
        <RemoveFormatting />
      </IconButton>
      <Divider />
      {groups[2].map(button)}
      <DropdownMenu>
        <Tooltip>
          <TooltipTrigger asChild>
            <DropdownMenuTrigger aria-label="Alignment" className="flex size-8 cursor-pointer items-center justify-center rounded-lg text-fg-secondary transition-colors duration-160 hover:bg-muted hover:text-foreground aria-expanded:bg-muted max-md:size-11">
              <Align className="size-4" />
            </DropdownMenuTrigger>
          </TooltipTrigger>
          <TooltipContent>Alignment</TooltipContent>
        </Tooltip>
        <DropdownMenuContent className="w-44">
          {alignments.map((a) => (
            <DropdownMenuItem key={a.value} onSelect={() => chain().setTextAlign(a.value).run()}>
              <a.icon />
              {a.label}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
      <Divider />
      {button({ label: "Link", icon: Link2, active: state.link, run: setLink })}
      {signatures && (
        <DropdownMenu>
          <Tooltip>
            <TooltipTrigger asChild>
              <DropdownMenuTrigger aria-label="Insert signature" className="flex size-8 cursor-pointer items-center justify-center rounded-lg text-fg-secondary transition-colors duration-160 hover:bg-muted hover:text-foreground aria-expanded:bg-muted max-md:size-11">
                <SignatureIcon className="size-4" />
              </DropdownMenuTrigger>
            </TooltipTrigger>
            <TooltipContent>Insert signature</TooltipContent>
          </Tooltip>
          <DropdownMenuContent className="w-56">
            {signatures.map((sig) => (
              <DropdownMenuItem key={sig.id} onSelect={() => chain().insertContent(sig.html).run()}>
                {sig.name}
              </DropdownMenuItem>
            ))}
            <DropdownMenuItem asChild>
              <a href="/settings#signatures" target="_blank" rel="noopener">{signatures.length ? "Manage signatures" : "Create a signature"}</a>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  )
}

"use client"

import { ChevronDown, Info, ShieldAlert } from "lucide-react"
import { cn } from "@/lib/utils"
import { useId, useState } from "react"

import { StatusBadge } from "@/components/status-badge"
import type { Address, AuthResult, AuthResultValue, SenderInsight, SenderWarning } from "@/lib/types"

const strong = "font-semibold text-foreground break-words"

function warningText(w: SenderWarning, sender: Address, organization: string) {
  switch (w.kind) {
    case "spoofed":
      return (
        <>
          It says it&apos;s from <span className={strong}>{w.domain}</span>, a domain of {organization}, but it wasn&apos;t sent from your organization&apos;s mail server and
          sender verification didn&apos;t confirm it.
        </>
      )
    case "name":
      return (
        <>
          The sender has the same name as <span className={strong}>{w.name}</span> in {organization}, but writes from <span className={strong}>{sender.address}</span>.{" "}
          {w.name}&apos;s address is <span className={strong}>{w.address}</span>.
        </>
      )
    case "address-in-name":
      return (
        <>
          The sender&apos;s name shows <span className={strong}>{w.address}</span>, but the message came from <span className={strong}>{sender.address}</span>.
        </>
      )
    case "lookalike":
      return (
        <>
          <span className={strong}>{w.domain}</span> looks like <span className={strong}>{w.resembles}</span>, a domain of {organization}, but it&apos;s a different domain.
        </>
      )
  }
}

export function SenderNotice({ sender, from }: { sender: SenderInsight; from: Address }) {
  const titleId = useId()
  const firstContact = (
    <>
      You don&apos;t often get email from <span className={strong}>{from.address}</span>.
    </>
  )

  if (sender.warnings.length > 0) {
    return (
      <section aria-labelledby={titleId} className="flex gap-3 rounded-lg border border-warning/40 bg-warning/12 p-4 text-sm">
        <ShieldAlert aria-hidden className="size-4 shrink-0 translate-y-0.5 text-warning" />
        <div className="flex min-w-0 flex-col gap-2">
          <h3 id={titleId} className="font-semibold">
            Check this sender before you reply
          </h3>
          <ul className={cn("flex flex-col gap-1 text-fg-secondary", sender.warnings.length > 1 && "list-disc ps-4")}>
            {sender.warnings.map((w) => (
              <li key={w.kind}>{warningText(w, from, sender.organization)}</li>
            ))}
            {sender.firstContact && <li>{firstContact}</li>}
          </ul>
          <p className="text-fg-secondary">If anything is unexpected, confirm with the sender another way before you open links or attachments, or send money or information.</p>
        </div>
      </section>
    )
  }

  if (!sender.firstContact) return null
  return (
    <p className="flex gap-3 rounded-lg bg-muted p-3 text-sm text-fg-secondary">
      <Info aria-hidden className="size-4 shrink-0 translate-y-0.5 text-fg-tertiary" />
      <span>{firstContact} Make sure you know who it&apos;s from before you open links or attachments.</span>
    </p>
  )
}

const resultBadges: Record<AuthResultValue, { tone: "success" | "warning" | "danger" | "neutral"; label: string }> = {
  pass: { tone: "success", label: "Pass" },
  fail: { tone: "danger", label: "Fail" },
  softfail: { tone: "warning", label: "Soft fail" },
  neutral: { tone: "neutral", label: "Neutral" },
  none: { tone: "neutral", label: "None" },
  temperror: { tone: "warning", label: "Not checked" },
  permerror: { tone: "warning", label: "Error" },
}

const dnsFailed = "It couldn't be checked because a DNS lookup failed."

function spfText({ result, domain }: AuthResult) {
  const d = domain ?? "The sender's domain"
  switch (result) {
    case "pass":
      return `The server that sent it is allowed to send mail for ${d}.`
    case "fail":
      return `The server that sent it isn't allowed to send mail for ${d}.`
    case "softfail":
      return `${d} says the server that sent it probably isn't allowed to send its mail.`
    case "neutral":
      return `${d} doesn't say whether the server that sent it may send its mail.`
    case "none":
      return `${d} doesn't list which servers may send its mail.`
    case "temperror":
      return dnsFailed
    case "permerror":
      return `${d} lists its mail servers in a record with errors, so it couldn't be checked.`
  }
}

function dkimText({ result, domain }: AuthResult) {
  switch (result) {
    case "pass":
      return `Signed by ${domain ?? "the sender"} and not changed since.`
    case "fail":
      return `It has a signature${domain ? ` from ${domain}` : ""}, but the signature doesn't match. The message may have been changed on the way.`
    case "temperror":
      return "It couldn't be checked because the signing key couldn't be looked up."
    case "permerror":
      return "The signature or its key is malformed."
    default:
      return "It isn't signed."
  }
}

function dmarcText({ result }: AuthResult, fromDomain: string) {
  switch (result) {
    case "pass":
      return `${fromDomain}, the domain in the From address, matches a domain that passed SPF or DKIM.`
    case "fail":
      return `${fromDomain}, the domain in the From address, doesn't match a domain that passed SPF or DKIM.`
    case "temperror":
      return dnsFailed
    case "permerror":
      return `${fromDomain} publishes a DMARC policy with errors.`
    default:
      return `${fromDomain} doesn't publish a DMARC policy.`
  }
}

export function SenderVerification({ sender, from }: { sender: SenderInsight; from: Address }) {
  const [open, setOpen] = useState(false)
  const auth = sender.authentication
  const fromDomain = from.address.slice(from.address.lastIndexOf("@") + 1).toLowerCase()
  const rows = auth && [
    { label: "SPF", result: auth.spf, text: spfText(auth.spf) },
    { label: "DKIM", result: auth.dkim, text: dkimText(auth.dkim) },
    { label: "DMARC", result: auth.dmarc, text: dmarcText(auth.dmarc, fromDomain) },
  ]

  return (
    <div className="flex flex-col gap-2">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="-mx-1 flex min-h-8 w-fit items-center gap-1 rounded-md px-1 text-sm text-fg-tertiary transition-colors duration-160 hover:text-foreground"
      >
        Sender verification
        <ChevronDown aria-hidden className={cn("size-4 transition-transform duration-160", open && "rotate-180")} />
      </button>
      {open && (
        <div className="animate-content rounded-lg border border-border p-4 text-sm">
          {!auth ? (
            <p className="text-fg-secondary">Not checked. This message didn&apos;t arrive through this mail server, for example because it was imported or added by an email app.</p>
          ) : auth.submission === "pass" ? (
            <p className="text-fg-secondary">Sent by a signed-in account on this mail server. SPF, DKIM and DMARC only apply to mail from other servers.</p>
          ) : auth.submission === "relay" ? (
            <p className="text-fg-secondary">Forwarded by a mail rule on this server, so the original sender couldn&apos;t be checked again.</p>
          ) : (
            <dl className="grid grid-cols-[auto_1fr] items-start gap-x-3 gap-y-3 sm:grid-cols-[3.5rem_6rem_1fr]">
              {rows!.map((r) => (
                <div key={r.label} className="col-span-2 grid grid-cols-subgrid items-start gap-y-1 sm:col-span-3">
                  <dt className="text-label pt-1 text-fg-secondary">{r.label}</dt>
                  <dd className="flex flex-col gap-1 sm:contents">
                    <StatusBadge tone={resultBadges[r.result.result].tone}>{resultBadges[r.result.result].label}</StatusBadge>
                    <span className="break-words text-fg-secondary sm:pt-0.5">{r.text}</span>
                  </dd>
                </div>
              ))}
            </dl>
          )}
        </div>
      )}
    </div>
  )
}

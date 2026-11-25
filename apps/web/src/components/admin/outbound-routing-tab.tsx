"use client"

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus, Route } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/confirm-dialog"
import { Field } from "@/components/field"
import { FormDialog } from "@/components/form-dialog"
import { SectionHeader } from "@/components/page-header"
import { EmptyState, QueryView } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { api, errorMessage } from "@/lib/api"

const kinds = { relay: "Sender relay", transport: "Recipient transport", tls: "TLS policy" }
const modes = {
  none: "None — plaintext",
  may: "May — opportunistic TLS",
  encrypt: "Encrypt — require TLS",
  dane: "DANE — DNSSEC-backed TLS when available",
  verify: "Verify — validate certificate and hostname",
  secure: "Secure — validate certificate and destination",
}

type Rule = {
  kind: keyof typeof kinds
  name: string
  target: string
  tlsMode: string
  matchNames: string[]
  hasCredentials: boolean
}

const path = "/admin/outbound-routing"
const rulePath = (kind: Rule["kind"], name: string) => `${path}/${kind}/${encodeURIComponent(name.trim())}`

export function OutboundRoutingTab() {
  const qc = useQueryClient()
  const rules = useQuery({ queryKey: [path], queryFn: () => api<Rule[]>(path) })
  const [editing, setEditing] = useState<Rule | "new" | null>(null)
  const [removing, setRemoving] = useState<Rule | null>(null)
  const [kind, setKind] = useState<Rule["kind"]>("relay")
  const [tlsMode, setTLSMode] = useState("may")
  const rule = editing === "new" ? null : editing

  const save = useMutation({
    mutationFn: (data: FormData) => api<Rule>(rulePath(kind, String(data.get("name") ?? "")), {
      method: "PUT",
      body: {
        target: data.get("target") ?? "",
        username: data.get("username") ?? "",
        password: data.get("password") ?? "",
        tlsMode: kind === "tls" ? tlsMode : "",
        matchNames: String(data.get("matchNames") ?? "").split(",").map((name) => name.trim()).filter(Boolean),
      },
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [path] })
      setEditing(null)
      toast.success("Routing rule saved")
    },
  })
  const remove = useMutation({
    mutationFn: (rule: Rule) => api(rulePath(rule.kind, rule.name), { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [path] })
      setRemoving(null)
      toast.success("Routing rule removed")
    },
    onError: (error) => toast.error(errorMessage(error)),
  })

  function edit(rule: Rule | "new") {
    save.reset()
    setKind(rule === "new" ? "relay" : rule.kind)
    setTLSMode(rule === "new" ? "may" : rule.tlsMode || "may")
    setEditing(rule)
  }

  return (
    <section className="flex flex-col gap-4">
      <SectionHeader
        title="Outbound routing"
        description="Choose sender relays, fixed paths for recipient domains, and TLS policies for delivery destinations."
        actions={<Button onClick={() => edit("new")}><Plus aria-hidden /> Add rule</Button>}
      />
      <QueryView query={rules} empty={<EmptyState icon={Route} title="No routing rules" description="Outgoing mail uses the server’s default routing and TLS settings." />}>
        {(data) => (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Rule</TableHead>
                <TableHead>Domain or destination</TableHead>
                <TableHead>Settings</TableHead>
                <TableHead><span className="sr-only">Actions</span></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.map((item) => (
                <TableRow key={rulePath(item.kind, item.name)}>
                  <TableCell>{kinds[item.kind]}</TableCell>
                  <TableCell className="font-mono">{item.name}</TableCell>
                  <TableCell>
                    {item.target || item.tlsMode}
                    {item.hasCredentials && <span className="block text-sm text-fg-tertiary">Credentials saved</span>}
                    {item.matchNames.length > 0 && <span className="block text-sm text-fg-tertiary">{item.matchNames.join(", ")}</span>}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button variant="ghost" size="sm" onClick={() => edit(item)}>Edit</Button>
                    <Button variant="ghost" size="sm" onClick={() => setRemoving(item)}>Remove</Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </QueryView>

      {editing && (
        <FormDialog
          open
          onOpenChange={(open) => { if (!open) setEditing(null) }}
          title={rule ? "Edit routing rule" : "Add routing rule"}
          submitLabel="Save rule"
          pending={save.isPending}
          onSubmit={(event) => {
            event.preventDefault()
            if (event.currentTarget.reportValidity()) save.mutate(new FormData(event.currentTarget))
          }}
        >
          {!rule && (
            <Field id="routing-kind" label="Rule type">
              <Select value={kind} onValueChange={(value) => setKind(value as Rule["kind"])}>
                <SelectTrigger id="routing-kind"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {Object.entries(kinds).map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          )}
          <Field id="routing-name" label={kind === "relay" ? "Sender domain" : kind === "transport" ? "Recipient domain" : "Delivery destination"}>
            <Input id="routing-name" name="name" defaultValue={rule?.name} readOnly={!!rule} required spellCheck={false} />
            {kind === "tls" && <p className="text-sm text-fg-tertiary">Use the recipient domain for direct delivery or the exact SMTP endpoint for a fixed route.</p>}
          </Field>
          {kind !== "tls" && (
            <Field id="routing-target" label="SMTP endpoint">
              <Input id="routing-target" name="target" defaultValue={rule?.target} placeholder="[smtp.example.com]:587" required spellCheck={false} />
            </Field>
          )}
          {kind === "relay" && (
            <>
              <Field id="routing-username" label="SMTP username (optional)">
                <Input id="routing-username" name="username" autoComplete="off" spellCheck={false} />
              </Field>
              <Field id="routing-password" label="SMTP password (optional)">
                <Input id="routing-password" name="password" type="password" autoComplete="new-password" />
                {rule?.hasCredentials && <p className="text-sm text-fg-tertiary">Re-enter both credentials to keep authentication. Saving both fields blank removes the stored credentials.</p>}
              </Field>
            </>
          )}
          {kind === "tls" && (
            <Field id="routing-mode" label="TLS mode">
              <Select value={tlsMode} onValueChange={setTLSMode}>
                <SelectTrigger id="routing-mode"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {Object.entries(modes).map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          )}
          {kind === "tls" && (tlsMode === "verify" || tlsMode === "secure") && (
            <Field id="routing-match" label="Certificate hostnames (optional)" hint="Separate hostnames or dot-prefixed domain suffixes with commas.">
              <Input id="routing-match" name="matchNames" defaultValue={rule?.matchNames.join(", ")} spellCheck={false} />
            </Field>
          )}
          {save.isError && <p role="alert" className="text-sm text-danger">{errorMessage(save.error)}</p>}
        </FormDialog>
      )}
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(open) => { if (!open) setRemoving(null) }}
        title="Remove routing rule"
        description={`Remove the ${removing ? kinds[removing.kind].toLowerCase() : "rule"} for ${removing?.name ?? ""}?`}
        confirmLabel="Remove rule"
        pending={remove.isPending}
        onConfirm={() => { if (removing) remove.mutate(removing) }}
      />
    </section>
  )
}

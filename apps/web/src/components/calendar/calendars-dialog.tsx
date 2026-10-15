"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2, UserPlus, X } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { cn } from "cn";

import { ConfirmDialog } from "@/components/confirm-dialog";
import { IconButton } from "@/components/icon-button";
import { QueryView } from "@/components/states";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useSession } from "@/hooks/use-session";
import { api, errorMessage } from "@/lib/api";
import { calendarsQuery } from "@/lib/queries";
import type { CalendarInfo } from "@/lib/types";

const palette = [
  "#3b82f6",
  "#10b981",
  "#f59e0b",
  "#e11d48",
  "#0ea5e9",
  "#64748b",
];

const accessLabels = { owner: "Yours", write: "Can edit", read: "View only" };

function Swatch({ color }: { color: string }) {
  return (
    <span
      aria-hidden
      className="size-3 shrink-0 rounded-full"
      style={{ background: color || "var(--color-ember)" }}
    />
  );
}

export function CalendarsDialog({
  open,
  onOpenChange,
  hidden,
  onToggle,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  hidden: string[];
  onToggle: (id: string) => void;
}) {
  const qc = useQueryClient();
  const calendars = useQuery({ ...calendarsQuery, enabled: open });
  const [name, setName] = useState("");
  const [color, setColor] = useState(palette[0]);
  const [sharing, setSharing] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<CalendarInfo | null>(null);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["calendars"] });
    qc.invalidateQueries({ queryKey: ["calendar-events"] });
  };

  const create = useMutation({
    mutationFn: () =>
      api<CalendarInfo>("/calendar/calendars", {
        method: "POST",
        body: { name, color },
      }),
    onSuccess: () => {
      setName("");
      refresh();
    },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const remove = useMutation({
    mutationFn: (id: string) =>
      api(`/calendar/calendars/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      setDeleting(null);
      refresh();
    },
    onError: (error) => toast.error(errorMessage(error)),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>Calendars</DialogTitle>
          <DialogDescription>
            Choose which calendars to show, and share yours with people in your
            organization.
          </DialogDescription>
        </DialogHeader>
        <QueryView query={calendars}>
          {(list) => (
            <ul className="flex flex-col divide-y divide-border">
              {list.map((c) => (
                <li key={c.id} className="flex flex-col gap-3 py-3">
                  <div className="flex items-center gap-3">
                    <Switch
                      checked={!hidden.includes(c.id)}
                      onCheckedChange={() => onToggle(c.id)}
                      aria-label={`Show ${c.name}`}
                    />
                    <Swatch color={c.color} />
                    <div className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate font-semibold">{c.name}</span>
                      <span className="truncate text-sm text-fg-tertiary">
                        {c.owner
                          ? `${c.owner.name || c.owner.email} · ${accessLabels[c.access]}`
                          : c.shares?.length
                            ? `Shared with ${c.shares.length}`
                            : "Only you"}
                      </span>
                    </div>
                    {c.access === "owner" && (
                      <Button
                        size="sm"
                        variant="secondary"
                        aria-expanded={sharing === c.id}
                        onClick={() =>
                          setSharing(sharing === c.id ? null : c.id)
                        }
                      >
                        <UserPlus aria-hidden />
                        Share
                      </Button>
                    )}
                    {c.access === "owner" && c.id !== "personal" && (
                      <IconButton
                        label={`Delete ${c.name}`}
                        size="icon-sm"
                        onClick={() => setDeleting(c)}
                      >
                        <Trash2 />
                      </IconButton>
                    )}
                  </div>
                  {sharing === c.id && (
                    <SharePanel calendar={c} onChanged={refresh} />
                  )}
                </li>
              ))}
            </ul>
          )}
        </QueryView>
        <form
          className="flex flex-col gap-3 border-t border-border pt-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate();
          }}
        >
          <label htmlFor="new-calendar" className="text-label">
            New calendar
          </label>
          <div className="flex gap-2">
            <Input
              id="new-calendar"
              value={name}
              maxLength={100}
              placeholder="Team, Holidays, On call…"
              onChange={(e) => setName(e.target.value)}
            />
            <Button
              type="submit"
              variant="secondary"
              loading={create.isPending}
              disabled={!name.trim()}
            >
              Create
            </Button>
          </div>
          <div role="radiogroup" aria-label="Color" className="flex gap-2">
            {palette.map((p) => (
              <button
                key={p}
                type="button"
                role="radio"
                aria-checked={color === p}
                aria-label={p}
                onClick={() => setColor(p)}
                className={cn(
                  "size-8 rounded-full border-2 border-transparent transition-colors duration-160",
                  color === p && "border-foreground",
                )}
                style={{
                  background: p,
                  backgroundClip: "content-box",
                  padding: 4,
                }}
              />
            ))}
          </div>
        </form>
        <ConfirmDialog
          open={!!deleting}
          onOpenChange={(o) => !o && setDeleting(null)}
          title={`Delete “${deleting?.name ?? ""}”?`}
          description="Every event in this calendar is deleted, and people it was shared with lose access. This cannot be undone."
          confirmLabel="Delete calendar"
          pending={remove.isPending}
          onConfirm={() => deleting && remove.mutate(deleting.id)}
        />
      </DialogContent>
    </Dialog>
  );
}

function SharePanel({
  calendar,
  onChanged,
}: {
  calendar: CalendarInfo;
  onChanged: () => void;
}) {
  const session = useSession();
  const adminOrgs =
    session.data?.memberships.filter((m) => m.role !== "member") ?? [];
  const [target, setTarget] = useState("");
  const [access, setAccess] = useState<"read" | "write">("read");
  const orgId = target.startsWith("org:") ? target.slice(4) : "";

  const share = useMutation({
    mutationFn: () =>
      api(`/calendar/calendars/${calendar.id}/shares`, {
        method: "POST",
        body: orgId ? { orgId, access } : { email: target.trim(), access },
      }),
    onSuccess: () => {
      setTarget("");
      onChanged();
    },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const unshare = useMutation({
    mutationFn: (id: string) =>
      api(`/calendar/calendars/${calendar.id}/shares/${id}`, {
        method: "DELETE",
      }),
    onSuccess: onChanged,
    onError: (error) => toast.error(errorMessage(error)),
  });

  return (
    <div className="flex flex-col gap-3 rounded-lg bg-sunken p-3">
      {calendar.shares && calendar.shares.length > 0 && (
        <ul className="flex flex-col gap-1 text-sm">
          {calendar.shares.map((s) => (
            <li key={s.id} className="flex items-center justify-between gap-3">
              <span className="truncate">
                {s.orgName ? `Everyone in ${s.orgName}` : s.name || s.email}
              </span>
              <span className="flex items-center gap-2">
                <span className="text-fg-tertiary">
                  {accessLabels[s.access]}
                </span>
                <IconButton
                  label="Stop sharing"
                  size="icon-sm"
                  onClick={() => unshare.mutate(s.id)}
                  disabled={unshare.isPending}
                >
                  <X />
                </IconButton>
              </span>
            </li>
          ))}
        </ul>
      )}
      <form
        className="flex flex-wrap gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (target.trim()) share.mutate();
        }}
      >
        {orgId ? (
          <Button
            type="button"
            variant="secondary"
            className="min-w-0 flex-1 justify-between"
            onClick={() => setTarget("")}
          >
            <span className="truncate">
              Everyone in {adminOrgs.find((o) => o.orgId === orgId)?.orgName}
            </span>
            <X aria-hidden />
          </Button>
        ) : (
          <Input
            aria-label="Email address"
            type="email"
            placeholder="name@company.com"
            className="min-w-0 flex-1"
            value={target}
            onChange={(e) => setTarget(e.target.value)}
          />
        )}
        <Select
          value={access}
          onValueChange={(v) => setAccess(v as "read" | "write")}
        >
          <SelectTrigger aria-label="Access" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="read">View only</SelectItem>
            <SelectItem value="write">Can edit</SelectItem>
          </SelectContent>
        </Select>
        <Button
          type="submit"
          loading={share.isPending}
          disabled={!target.trim()}
        >
          Share
        </Button>
      </form>
      {!orgId && adminOrgs.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {adminOrgs.map((o) => (
            <Button
              key={o.orgId}
              type="button"
              size="sm"
              variant="quiet"
              onClick={() => setTarget(`org:${o.orgId}`)}
            >
              Share with everyone in {o.orgName}
            </Button>
          ))}
        </div>
      )}
    </div>
  );
}

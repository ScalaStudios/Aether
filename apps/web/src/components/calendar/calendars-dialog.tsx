"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, SlidersHorizontal, Trash2, UserPlus, X } from "lucide-react";
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
import { formatDateTime, plural } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { calendarsQuery } from "@/lib/queries";
import type { CalendarInfo, CalendarSubscription } from "@/lib/types";

const palette = [
  "#3b82f6",
  "#10b981",
  "#f59e0b",
  "#e11d48",
  "#0ea5e9",
  "#64748b",
];

const refreshes = [
  { value: "15", label: "Every 15 minutes" },
  { value: "60", label: "Hourly" },
  { value: "360", label: "Every 6 hours" },
  { value: "1440", label: "Daily" },
  { value: "10080", label: "Weekly" },
];


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
  const { t } = useI18n();
  const qc = useQueryClient();
  const calendars = useQuery({ ...calendarsQuery, enabled: open });
  const [name, setName] = useState("");
  const [color, setColor] = useState(palette[0]);
  const [sharing, setSharing] = useState<string | null>(null);
  const [editingFeed, setEditingFeed] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<CalendarInfo | null>(null);
  const [feedUrl, setFeedUrl] = useState("");
  const [feedName, setFeedName] = useState("");
  const [feedColor, setFeedColor] = useState(palette[2]);
  const [feedRefresh, setFeedRefresh] = useState("360");
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["calendars"] });
    qc.invalidateQueries({ queryKey: ["calendar-events"] });
    qc.invalidateQueries({ queryKey: ["tasks"] });
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
  const subscribe = useMutation({
    mutationFn: () =>
      api<CalendarInfo>("/calendar/subscriptions", {
        method: "POST",
        body: { url: feedUrl.trim(), name: feedName.trim(), color: feedColor, refreshMinutes: Number(feedRefresh) },
      }),
    onSuccess: (calendar) => {
      setFeedUrl("");
      setFeedName("");
      refresh();
      toast.success("Calendar subscribed", {
        description: `${plural(calendar.subscription?.events ?? 0, "event")} added.`,
      });
    },
  });
  const refreshFeed = useMutation({
    mutationFn: (id: string) =>
      api<CalendarSubscription>(`/calendar/subscriptions/${encodeURIComponent(id)}/refresh`, { method: "POST" }),
    onSuccess: (info) => {
      refresh();
      toast.success(info.notModified ? "Calendar is up to date" : "Calendar refreshed", {
        description: `${plural(info.events ?? 0, "event")} on this calendar.`,
      });
    },
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="grid-cols-[minmax(0,1fr)] sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>{t("calendar.calendars.title")}</DialogTitle>
          <DialogDescription>
            Choose which calendars to show, share yours, or subscribe to a calendar link.
          </DialogDescription>
        </DialogHeader>
        <QueryView query={calendars}>
          {(list) => (
            <ul className="flex min-w-0 flex-col divide-y divide-border">
              {list.map((c) => (
                <li key={c.id} className="flex min-w-0 flex-col gap-3 py-3">
                  <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
                    <div className="flex min-w-[180px] flex-1 items-center gap-3">
                      <Switch
                        checked={!hidden.includes(c.id)}
                        onCheckedChange={() => onToggle(c.id)}
                        aria-label={t("calendar.calendars.showCalendar", { name: c.name })}
                      />
                      <Swatch color={c.color} />
                      <div className="flex min-w-0 flex-1 flex-col">
                        <span className="truncate font-semibold">{c.name}</span>
                        <span className={cn("text-sm", c.subscription?.lastError ? "text-danger" : "truncate text-fg-tertiary")}>
                          {c.subscription?.lastError
                            ? c.subscription.lastError
                            : c.subscribed
                              ? c.subscription?.lastFetchedAt
                                ? `Subscribed · updated ${formatDateTime(c.subscription.lastFetchedAt)}`
                                : "Subscribed from a link"
                              : c.owner
                                ? `${c.owner.name || c.owner.email} · ${t(`calendar.calendars.access.${c.access}`)}`
                                : c.shares?.length
                                  ? t("calendar.calendars.sharedWith", { count: c.shares.length })
                                  : t("calendar.calendars.onlyYou")}
                        </span>
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
                      {c.subscription && (
                        <>
                          <IconButton
                            label={`Refresh ${c.name}`}
                            size="icon-sm"
                            onClick={() => refreshFeed.mutate(c.id)}
                            disabled={refreshFeed.isPending}
                          >
                            <RefreshCw className={refreshFeed.isPending && refreshFeed.variables === c.id ? "animate-spin" : undefined} />
                          </IconButton>
                          <IconButton
                            label={`Subscription settings for ${c.name}`}
                            size="icon-sm"
                            aria-expanded={editingFeed === c.id}
                            onClick={() => {
                              setEditingFeed(editingFeed === c.id ? null : c.id);
                              setSharing(null);
                            }}
                          >
                            <SlidersHorizontal />
                          </IconButton>
                        </>
                      )}
                      {c.access === "owner" && (
                        <Button
                          size="sm"
                          variant="secondary"
                          aria-expanded={sharing === c.id}
                          onClick={() => {
                            setSharing(sharing === c.id ? null : c.id);
                            setEditingFeed(null);
                          }}
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

                  </div>
                  {editingFeed === c.id && c.subscription && (
                    <SubscriptionPanel
                      calendar={c}
                      subscription={c.subscription}
                      onSaved={() => {
                        setEditingFeed(null);
                        refresh();
                      }}
                    />
                  )}
                  {sharing === c.id && (
                    <SharePanel calendar={c} onChanged={refresh} />
                  )}
                </li>
              ))}
            </ul>
          )}
        </QueryView>
        <form
          className="flex min-w-0 flex-col gap-3 border-t border-border pt-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate();
          }}
        >
          <label htmlFor="new-calendar" className="text-label">
            {t("calendar.calendars.newCalendar")}
          </label>
          <div className="flex gap-2">
            <Input
              id="new-calendar"
              value={name}
              maxLength={100}
              placeholder={t("calendar.calendars.newCalendarPlaceholder")}
              onChange={(e) => setName(e.target.value)}
            />
            <Button
              type="submit"
              variant="secondary"
              loading={create.isPending}
              disabled={!name.trim()}
            >
              {t("calendar.calendars.create")}
            </Button>
          </div>
          <div role="radiogroup" aria-label={t("calendar.calendars.color")} className="flex gap-2">
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
        <form
          className="flex min-w-0 flex-col gap-3 border-t border-border pt-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (feedUrl.trim()) subscribe.mutate();
          }}
        >
          <div className="flex flex-col gap-1">
            <label htmlFor="feed-url" className="text-label">
              Subscribe from a link
            </label>
            <p className="text-sm text-fg-tertiary">
              Paste an ICS address. Aether refreshes it on a schedule. webcal links work too. The calendar stays read-only.
            </p>
          </div>
          <Input
            id="feed-url"
            type="url"
            value={feedUrl}
            placeholder="https://example.com/calendar.ics"
            onChange={(e) => setFeedUrl(e.target.value)}
          />
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              aria-label="Calendar name"
              value={feedName}
              maxLength={100}
              placeholder="Holidays, Team leave…"
              onChange={(e) => setFeedName(e.target.value)}
            />
            <Select value={feedRefresh} onValueChange={setFeedRefresh}>
              <SelectTrigger aria-label="Refresh schedule" className="w-full sm:w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {refreshes.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div role="radiogroup" aria-label="Subscription color" className="flex gap-2">
            {palette.map((p) => (
              <button
                key={p}
                type="button"
                role="radio"
                aria-checked={feedColor === p}
                aria-label={p}
                onClick={() => setFeedColor(p)}
                className={cn(
                  "size-8 rounded-full border-2 border-transparent transition-colors duration-160",
                  feedColor === p && "border-foreground",
                )}
                style={{ background: p, backgroundClip: "content-box", padding: 4 }}
              />
            ))}
          </div>
          <Button type="submit" variant="secondary" className="self-start" loading={subscribe.isPending} disabled={!feedUrl.trim()}>
            Subscribe
          </Button>
        </form>
        <ConfirmDialog
          open={!!deleting}
          onOpenChange={(o) => !o && setDeleting(null)}
          title={t("calendar.calendars.deleteTitle", { name: deleting?.name ?? "" })}
          description={
            deleting?.subscribed
              ? "This stops the subscription and removes the events it added. This cannot be undone."
              : "Every event in this calendar is deleted, and people it was shared with lose access. This cannot be undone."
          }
          confirmLabel={t("calendar.calendars.deleteConfirm")}
          pending={remove.isPending}
          onConfirm={() => deleting && remove.mutate(deleting.id)}
        />
      </DialogContent>
    </Dialog>
  );
}

function SubscriptionPanel({
  calendar,
  subscription,
  onSaved,
}: {
  calendar: CalendarInfo;
  subscription: CalendarSubscription;
  onSaved: () => void;
}) {
  const [url, setUrl] = useState(subscription.url);
  const [interval, setInterval] = useState(String(subscription.refreshMinutes));
  const options = refreshes.some((r) => r.value === interval)
    ? refreshes
    : [...refreshes, { value: interval, label: `Every ${interval} minutes` }];
  const update = useMutation({
    mutationFn: () =>
      api(`/calendar/subscriptions/${encodeURIComponent(calendar.id)}`, {
        method: "PATCH",
        body: { url: url.trim(), refreshMinutes: Number(interval) },
      }),
    onSuccess: () => {
      toast.success("Subscription saved");
      onSaved();
    },
  });

  return (
    <form
      className="flex flex-col gap-3 rounded-lg bg-sunken p-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (url.trim()) update.mutate();
      }}
    >
      <Input
        aria-label="Subscription link"
        type="url"
        value={url}
        onChange={(e) => setUrl(e.target.value)}
      />
      <div className="flex flex-wrap items-center gap-2">
        <Select value={interval} onValueChange={setInterval}>
          <SelectTrigger aria-label="Refresh schedule" className="w-full sm:w-48">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button type="submit" size="sm" loading={update.isPending} disabled={!url.trim()}>
          Save
        </Button>
      </div>
    </form>
  );
}

function SharePanel({
  calendar,
  onChanged,
}: {
  calendar: CalendarInfo;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const session = useSession();
  const adminOrgs =
    session.data?.memberships.filter((m) => m.role === "owner" || m.role === "admin") ?? [];
  const [target, setTarget] = useState("");
  const [access, setAccess] = useState<"read" | "write">("read");
  const orgId = target.startsWith("org:") ? target.slice(4) : "";
  const effectiveAccess = calendar.subscribed ? "read" : access;

  const share = useMutation({
    mutationFn: () =>
      api(`/calendar/calendars/${calendar.id}/shares`, {
        method: "POST",
        body: orgId ? { orgId, access: effectiveAccess } : { email: target.trim(), access: effectiveAccess },
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
                {s.orgName ? t("calendar.calendars.everyoneIn", { org: s.orgName }) : s.name || s.email}
              </span>
              <span className="flex items-center gap-2">
                <span className="text-fg-tertiary">
                  {t(`calendar.calendars.access.${s.access}`)}
                </span>
                <IconButton
                  label={t("calendar.calendars.stopSharing")}
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
              {t("calendar.calendars.everyoneIn", {
                org: adminOrgs.find((o) => o.orgId === orgId)?.orgName ?? "",
              })}
            </span>
            <X aria-hidden />
          </Button>
        ) : (
          <Input
            aria-label={t("calendar.calendars.emailAddress")}
            type="email"
            placeholder={t("calendar.calendars.emailPlaceholder")}
            className="min-w-0 flex-1"
            value={target}
            onChange={(e) => setTarget(e.target.value)}
          />
        )}
        {!calendar.subscribed && (
          <Select
            value={access}
            onValueChange={(v) => setAccess(v as "read" | "write")}
          >
            <SelectTrigger aria-label={t("calendar.calendars.accessLabel")} className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="read">{t("calendar.calendars.access.read")}</SelectItem>
              <SelectItem value="write">{t("calendar.calendars.access.write")}</SelectItem>
            </SelectContent>
          </Select>
        )}
        <Button
          type="submit"
          loading={share.isPending}
          disabled={!target.trim()}
        >
          {t("calendar.calendars.share")}
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
              {t("calendar.calendars.shareWithOrg", { org: o.orgName })}
            </Button>
          ))}
        </div>
      )}
    </div>
  );
}

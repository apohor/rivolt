import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  backend,
  MachineError,
  type PreheatSchedule,
  type PreheatScheduleInput,
} from "../lib/api";
import { Card, ErrorBox, Spinner } from "./ui";

// Sun=bit0, Mon=bit1, ... Sat=bit6 — matches the backend's bitmask.
const DAYS = [
  { bit: 1 << 1, short: "M" },
  { bit: 1 << 2, short: "T" },
  { bit: 1 << 3, short: "W" },
  { bit: 1 << 4, short: "T" },
  { bit: 1 << 5, short: "F" },
  { bit: 1 << 6, short: "S" },
  { bit: 1 << 0, short: "S" },
];
const WEEKDAYS = (1 << 1) | (1 << 2) | (1 << 3) | (1 << 4) | (1 << 5);

function maskLabel(mask: number): string {
  if (mask === 0b1111111) return "Every day";
  if (mask === WEEKDAYS) return "Weekdays";
  if (mask === ((1 << 0) | (1 << 6))) return "Weekends";
  return DAYS.filter((d) => mask & d.bit)
    .map((d) => d.short)
    .join(" ");
}

function formatNext(iso?: string): string {
  if (!iso || iso.startsWith("0001")) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (sameDay) return `Today, ${time}`;
  return `${d.toLocaleDateString([], { weekday: "short" })} ${time}`;
}

const EMPTY: PreheatScheduleInput = {
  name: "Morning",
  enabled: true,
  time_of_day: "07:30",
  weekday_mask: WEEKDAYS,
};

// Preheat configuration section, used inside SettingsPage. This section is
// purely about scheduling — the Home page has its own "Preheat" pill button
// for ad-hoc triggering, so we don't duplicate it here.
export default function PreheatSection() {
  const qc = useQueryClient();
  const schedules = useQuery({
    queryKey: ["preheat", "schedules"],
    queryFn: () => backend.listPreheatSchedules(),
  });
  const status = useQuery({
    queryKey: ["preheat", "status"],
    queryFn: () => backend.preheatStatus(),
    refetchInterval: 30_000,
  });

  const create = useMutation({
    mutationFn: (input: PreheatScheduleInput) => backend.createPreheatSchedule(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["preheat"] }),
  });

  const update = useMutation({
    mutationFn: ({ id, input }: { id: string; input: PreheatScheduleInput }) =>
      backend.updatePreheatSchedule(id, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["preheat"] }),
  });

  const remove = useMutation({
    mutationFn: (id: string) => backend.deletePreheatSchedule(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["preheat"] }),
  });

  const [draft, setDraft] = useState<PreheatScheduleInput | null>(null);

  const lastTriggeredText = useMemo(() => {
    const t = status.data?.last_triggered;
    if (!t || t.startsWith("0001")) return "never";
    return new Date(t).toLocaleString();
  }, [status.data?.last_triggered]);

  // Schedules are stored as HH:MM and fire in the server's local timezone
  // (controlled by the container's TZ env). If that doesn't match the
  // browser, "7:10 AM" on the row is server-local and will fire at a
  // different wall-clock time for the user. Surface that mismatch rather
  // than silently drift.
  const browserTZ = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const serverTZ = status.data?.timezone;
  const tzMismatch =
    !!serverTZ && serverTZ !== browserTZ && !(serverTZ === "Local" && browserTZ);

  return (
    <Card id="preheat-schedule" title="Preheat schedule">
      <p className="text-xs text-neutral-500">
        Warm the machine on a schedule so it's pull-ready when you walk up.
        Trigger an ad-hoc preheat from the Home page.
      </p>

      <div className="mt-4 grid gap-3 sm:grid-cols-3">
        <StatusTile label="Last triggered" value={lastTriggeredText} hint={status.data?.last_source} />
        <StatusTile
          label="Next scheduled"
          value={formatNext(status.data?.next_scheduled)}
          hint={status.data?.next_schedule}
        />
        <StatusTile
          label="State"
          value={status.data?.last_error ? "error" : "idle"}
          hint={status.data?.last_error}
          tone={status.data?.last_error ? "rose" : "neutral"}
        />
      </div>

      {tzMismatch && (
        <div className="mt-3 rounded-md border border-amber-900/60 bg-amber-950/30 px-3 py-2 text-xs text-amber-200">
          Schedule times are interpreted in the server's timezone{" "}
          <span className="font-mono">{serverTZ}</span>, but your browser is in{" "}
          <span className="font-mono">{browserTZ}</span>. A schedule set to{" "}
          <span className="font-mono">07:10</span> will fire at 07:10{" "}
          {serverTZ}, not in your local time. Set <span className="font-mono">TZ</span>{" "}
          on the caffeine container (e.g. <span className="font-mono">TZ={browserTZ}</span>)
          and restart it to align.
        </div>
      )}

      <div className="mt-6 flex items-center justify-between">
        <h3 className="text-sm font-semibold text-neutral-200">Schedules</h3>
        <button
          type="button"
          onClick={() => setDraft(EMPTY)}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1 text-sm hover:bg-neutral-800"
        >
          + New schedule
        </button>
      </div>

      {schedules.isLoading && <Spinner />}
      {schedules.error && (
        <ErrorBox title="Could not load schedules" detail={String(schedules.error)} />
      )}

      {draft && (
        <ScheduleForm
          value={draft}
          onCancel={() => setDraft(null)}
          onSubmit={(input) => {
            create.mutate(input, { onSuccess: () => setDraft(null) });
          }}
          submitting={create.isPending}
          error={create.error}
        />
      )}

      <ul className="mt-4 divide-y divide-neutral-800">
        {(schedules.data ?? []).length === 0 && !schedules.isLoading && (
          <li className="py-6 text-sm text-neutral-500">
            No schedules yet. Create one above to have caffeine preheat the machine for you.
          </li>
        )}
        {(schedules.data ?? []).map((sch) => (
          <ScheduleRow
            key={sch.id}
            sch={sch}
            onToggle={(enabled) =>
              update.mutate({
                id: sch.id,
                input: {
                  name: sch.name,
                  enabled,
                  time_of_day: sch.time_of_day,
                  weekday_mask: sch.weekday_mask,
                },
              })
            }
            onSave={(input) => update.mutate({ id: sch.id, input })}
            onDelete={() => remove.mutate(sch.id)}
          />
        ))}
      </ul>
    </Card>
  );
}

function StatusTile({
  label,
  value,
  hint,
  tone = "neutral",
}: {
  label: string;
  value: string;
  hint?: string;
  tone?: "neutral" | "rose";
}) {
  const valueColor = tone === "rose" ? "text-rose-300" : "text-neutral-100";
  return (
    <div className="rounded-lg border border-neutral-800 bg-neutral-900/50 p-3">
      <div className="text-[11px] uppercase tracking-wide text-neutral-500">{label}</div>
      <div className={`mt-1 text-sm font-medium ${valueColor}`}>{value}</div>
      {hint && <div className="mt-1 truncate text-xs text-neutral-500">{hint}</div>}
    </div>
  );
}

function ScheduleRow({
  sch,
  onToggle,
  onSave,
  onDelete,
}: {
  sch: PreheatSchedule;
  onToggle: (enabled: boolean) => void;
  onSave: (input: PreheatScheduleInput) => void;
  onDelete: () => void;
}) {
  const [editing, setEditing] = useState(false);
  if (editing) {
    return (
      <li className="py-3">
        <ScheduleForm
          value={{
            name: sch.name,
            enabled: sch.enabled,
            time_of_day: sch.time_of_day,
            weekday_mask: sch.weekday_mask,
          }}
          onCancel={() => setEditing(false)}
          onSubmit={(input) => {
            onSave(input);
            setEditing(false);
          }}
        />
      </li>
    );
  }
  return (
    <li className="flex items-center justify-between py-3">
      <div className="flex items-center gap-4">
        <button
          type="button"
          onClick={() => onToggle(!sch.enabled)}
          className={`relative h-5 w-9 rounded-full transition-colors ${
            sch.enabled ? "bg-emerald-600" : "bg-neutral-700"
          }`}
          aria-label={sch.enabled ? "disable" : "enable"}
        >
          <span
            className={`absolute top-0.5 h-4 w-4 rounded-full bg-white transition-all ${
              sch.enabled ? "left-4" : "left-0.5"
            }`}
          />
        </button>
        <div>
          <div className="text-sm font-medium text-neutral-100">{sch.name}</div>
          <div className="text-xs text-neutral-500">
            {sch.time_of_day} • {maskLabel(sch.weekday_mask)}
          </div>
        </div>
      </div>
      <div className="flex gap-2">
        <button
          type="button"
          onClick={() => setEditing(true)}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1 text-xs hover:bg-neutral-800"
        >
          Edit
        </button>
        <button
          type="button"
          onClick={onDelete}
          className="rounded-md border border-rose-900/60 bg-rose-950/30 px-2.5 py-1 text-xs text-rose-300 hover:bg-rose-950/60"
        >
          Delete
        </button>
      </div>
    </li>
  );
}

function ScheduleForm({
  value,
  onCancel,
  onSubmit,
  submitting,
  error,
}: {
  value: PreheatScheduleInput;
  onCancel: () => void;
  onSubmit: (input: PreheatScheduleInput) => void;
  submitting?: boolean;
  error?: unknown;
}) {
  const [local, setLocal] = useState<PreheatScheduleInput>(value);
  const toggleDay = (bit: number) =>
    setLocal((s) => ({ ...s, weekday_mask: s.weekday_mask ^ bit }));
  const setMask = (mask: number) => setLocal((s) => ({ ...s, weekday_mask: mask }));

  return (
    <form
      className="mt-3 rounded-md border border-neutral-800 bg-neutral-950 p-3"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit(local);
      }}
    >
      <div className="grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end">
        <label className="block">
          <span className="text-xs text-neutral-500">Name</span>
          <input
            type="text"
            value={local.name}
            onChange={(e) => setLocal((s) => ({ ...s, name: e.target.value }))}
            className="mt-1 w-full rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm"
          />
        </label>
        <label className="block">
          <span className="text-xs text-neutral-500">Time (24h)</span>
          <input
            type="time"
            value={local.time_of_day}
            onChange={(e) => setLocal((s) => ({ ...s, time_of_day: e.target.value }))}
            className="mt-1 w-full rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm"
            required
          />
        </label>
      </div>

      <div className="mt-3">
        <span className="text-xs text-neutral-500">Days</span>
        <div className="mt-1 flex flex-wrap items-center gap-2">
          {DAYS.map((d, i) => {
            const on = !!(local.weekday_mask & d.bit);
            return (
              <button
                key={i}
                type="button"
                onClick={() => toggleDay(d.bit)}
                className={`h-8 w-8 rounded-md text-sm font-medium transition-colors ${
                  on
                    ? "bg-emerald-600 text-white"
                    : "border border-neutral-700 bg-neutral-900 text-neutral-400 hover:bg-neutral-800"
                }`}
                aria-pressed={on}
              >
                {d.short}
              </button>
            );
          })}
          <span className="ml-2 text-xs text-neutral-600">
            {local.weekday_mask === 0 ? "(none)" : maskLabel(local.weekday_mask)}
          </span>
        </div>
        <div className="mt-2 flex gap-2 text-xs">
          <button
            type="button"
            onClick={() => setMask(WEEKDAYS)}
            className="text-neutral-500 hover:text-neutral-200"
          >
            Weekdays
          </button>
          <button
            type="button"
            onClick={() => setMask((1 << 0) | (1 << 6))}
            className="text-neutral-500 hover:text-neutral-200"
          >
            Weekends
          </button>
          <button
            type="button"
            onClick={() => setMask(0b1111111)}
            className="text-neutral-500 hover:text-neutral-200"
          >
            Every day
          </button>
        </div>
      </div>

      {error ? (
        <div className="mt-3 rounded-md border border-rose-900/60 bg-rose-950/30 p-2 text-xs text-rose-300">
          {error instanceof MachineError
            ? `${error.status}: ${JSON.stringify(error.body)}`
            : String(error)}
        </div>
      ) : null}

      <div className="mt-3 flex justify-end gap-2">
        <button
          type="button"
          onClick={onCancel}
          className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800"
        >
          Cancel
        </button>
        <button
          type="submit"
          disabled={submitting || local.weekday_mask === 0 || !local.name.trim()}
          className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
        >
          {submitting ? "saving…" : "Save"}
        </button>
      </div>
    </form>
  );
}

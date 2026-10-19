import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type EngagementSettings } from "../lib/api";

// The browser's own zone goes with every save: a reminder at 8 PM has to mean the user's 8 PM, and
// the server never sends one for an account that has not stored a zone.
export const browserZone = (): string => Intl.DateTimeFormat().resolvedOptions().timeZone;

// useEngagement: the account's alert settings. Every save carries the browser's time zone.
export function useEngagement() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["engagement"], queryFn: () => backend.engagementGet(), staleTime: 30_000 });
  const put = useMutation({
    mutationFn: (patch: Partial<Omit<EngagementSettings, "timezone_stored">>) => backend.engagementPut({ ...patch, timezone: browserZone() }),
    onSuccess: (d) => qc.setQueryData(["engagement"], d),
  });
  return { settings: q.data, loading: q.isLoading, loadError: q.isError, save: put.mutateAsync, saving: put.isPending };
}

const THRESHOLD_MIN = 5;
const THRESHOLD_MAX = 95;
export const LATEST_REMIND_AT = "22:00";

// PlugReminderFields: when the reminder fires. Account-wide, and only shown while this device has the
// reminder on. A value the server would refuse is held back here with a plain sentence.
export function PlugReminderFields({
  settings,
  hasHome,
  onSave,
  onStop,
  stopLabel,
  busy,
}: {
  settings: EngagementSettings;
  hasHome: boolean;
  onSave: (patch: { plug_threshold_pct?: number; plug_remind_at?: string }) => Promise<void>;
  onStop: () => void;
  stopLabel: string;
  busy: boolean;
}) {
  const [pct, setPct] = useState(String(settings.plug_threshold_pct));
  const [at, setAt] = useState(settings.plug_remind_at);
  const [problem, setProblem] = useState<string | null>(null);

  async function commit(patch: { plug_threshold_pct?: number; plug_remind_at?: string }) {
    setProblem(null);
    try {
      await onSave(patch);
    } catch (e) {
      setProblem(e instanceof Error ? e.message : String(e));
    }
  }
  function commitPct() {
    const n = Number(pct);
    if (!Number.isFinite(n) || n < THRESHOLD_MIN || n > THRESHOLD_MAX) {
      setProblem(`Pick a level from ${THRESHOLD_MIN}% to ${THRESHOLD_MAX}%.`);
      return;
    }
    if (n !== settings.plug_threshold_pct) void commit({ plug_threshold_pct: Math.round(n) });
  }
  function commitAt() {
    if (!/^\d\d:\d\d$/.test(at) || at > LATEST_REMIND_AT) {
      setProblem("Pick a time no later than 10:00 PM.");
      return;
    }
    if (at !== settings.plug_remind_at) void commit({ plug_remind_at: at });
  }

  return (
    <div className="space-y-2 rounded-md border border-neutral-800 bg-neutral-950 p-3 text-sm" data-plug-reminder>
      <p className="text-xs text-neutral-400">Never more than one a day.</p>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
        <label className="flex items-center gap-2 text-neutral-300">
          Remind me below
          <input
            type="number"
            min={THRESHOLD_MIN}
            max={THRESHOLD_MAX}
            step={5}
            value={pct}
            onChange={(e) => setPct(e.target.value)}
            onBlur={commitPct}
            className="w-16 rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-0.5 tabular-nums text-neutral-100"
          />
          %
        </label>
        <label className="flex items-center gap-2 text-neutral-300">
          At
          <input
            type="time"
            max={LATEST_REMIND_AT}
            value={at}
            onChange={(e) => setAt(e.target.value)}
            onBlur={commitAt}
            className="rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-0.5 tabular-nums text-neutral-100"
          />
        </label>
      </div>
      {!hasHome && (
        <p className="text-xs text-amber-300/90">Set your home place in Settings → Places first; the reminder only fires there.</p>
      )}
      {problem && (
        <p role="alert" className="text-xs text-rose-400">
          {problem}
        </p>
      )}
      <button
        type="button"
        onClick={onStop}
        disabled={busy}
        className="text-xs text-neutral-400 underline hover:text-neutral-200 disabled:opacity-50"
      >
        {stopLabel}
      </button>
    </div>
  );
}

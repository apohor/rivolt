import { useEffect, useId, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, backend } from "../lib/api";
import { ErrorBox, Spinner } from "./ui";
import {
  DAYS_LABEL,
  LABEL_MAX,
  MAX_WINDOWS,
  RATE_MAX,
  coverage,
  describeWindow,
  hasErrors,
  serverFieldError,
  toDraft,
  toWindows,
  validateWindows,
  type DraftWindow,
  type OffpeakDays,
} from "../lib/offpeak";

const field = "rounded-sm border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-200";
const browserTz = () => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
};
const zones = (current: string): string[] => {
  let all: string[];
  try {
    all = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  } catch {
    all = [];
  }
  return all.includes(current) ? all : [current, ...all];
};
const newWindow = (): DraftWindow => ({ days: "all", start: "21:00", end: "06:00", rate: "", label: "" });

// OffpeakPanel edits the off-peak schedule: several windows, each with the days it covers, a start, an end (past midnight is fine) and a
// rate, in one time zone. Hours outside every window are priced at the home rate.
export function OffpeakPanel() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["offpeak"], queryFn: () => backend.getOffpeak() });
  const settings = useQuery({ queryKey: ["charging-settings"], queryFn: () => backend.getChargingSettings() });
  const [rows, setRows] = useState<DraftWindow[]>([]);
  const [tz, setTz] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [saved, setSaved] = useState(false);
  const [dirty, setDirty] = useState(false);
  const uid = useId();
  useEffect(() => {
    if (!loaded && q.data) {
      setRows(toDraft(q.data));
      setTz(q.data.tz || browserTz());
      setLoaded(true);
    }
  }, [q.data, loaded]);
  const errs = useMemo(() => validateWindows(rows), [rows]);
  const currency = q.data?.currency || settings.data?.home_currency || "USD";
  const homeRate = q.data?.base_rate ?? settings.data?.home_price_per_kwh ?? 0;

  const save = useMutation({
    mutationFn: (windows: DraftWindow[]) => backend.setOffpeak({ tz, windows: toWindows(windows) }),
    onSuccess: (res, windows) => {
      qc.invalidateQueries({ queryKey: ["offpeak"] });
      qc.invalidateQueries({ queryKey: ["charges"] });
      qc.invalidateQueries({ queryKey: ["ledger"] });
      if (res && Array.isArray(res.windows)) setRows(toDraft(res));
      else setRows(windows);
      setDirty(false);
      setSaved(true);
    },
  });
  // A refusal from the server, placed on the row it names (the client checks should catch these first).
  const refusal = save.error instanceof ApiError ? serverFieldError(save.error.body) : null;
  const edit = (i: number, patch: Partial<DraftWindow>) => {
    setRows((prev) => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
    setDirty(true);
    setSaved(false);
    save.reset();
  };
  const touch = (next: DraftWindow[]) => {
    setRows(next);
    setDirty(true);
    setSaved(false);
    save.reset();
  };

  const flat = q.data?.flat_rate === true;
  const setFlat = useMutation({
    mutationFn: (on: boolean) => backend.setFlatRate(on),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["offpeak"] });
      qc.invalidateQueries({ queryKey: ["savings"] });
      qc.invalidateQueries({ queryKey: ["ledger"] });
    },
  });

  if (q.isLoading || !loaded) return q.isError ? <ErrorBox title="Failed to load" detail={String(q.error)} /> : <Spinner />;
  const bad = hasErrors(errs);
  const strips: { kind: "weekdays" | "weekends"; label: string }[] = [
    { kind: "weekdays", label: "Weekdays" },
    { kind: "weekends", label: "Weekends" },
  ];

  return (
    <form
      className="space-y-3 text-sm"
      data-offpeak
      onSubmit={(e) => {
        e.preventDefault();
        if (!bad) save.mutate(rows);
      }}
    >
      <label data-flat-rate className="flex items-start gap-2 text-sm text-neutral-200">
        <input
          type="checkbox"
          className="mt-1 accent-emerald-500"
          checked={flat}
          disabled={setFlat.isPending}
          onChange={(e) => setFlat.mutate(e.target.checked)}
        />
        <span>
          My electricity is one flat rate (no off-peak hours)
          <span className="block text-xs text-neutral-400">
            {flat
              ? "Rivolt shows no off-peak savings, no off-peak line on share cards and no saving in your statement email. Your hours below are kept, and the estimates come back if you turn this off."
              : "Turn this on if your utility has no cheaper hours, so Rivolt stops offering off-peak estimates."}
          </span>
        </span>
      </label>
      {setFlat.isError && (
        <p role="alert" className="text-xs text-rose-400">Couldn&apos;t save that. Try again in a moment.</p>
      )}
      <p className="text-xs text-neutral-400">
        If your utility charges less at certain hours, add those hours and their rate. You enter them yourself. Rivolt uses them only for estimates in Settings
        and Money; it does not contact your utility and cannot check them. Hours outside every window use your home rate
        {homeRate > 0 ? ` (${homeRate} ${currency}/kWh)` : ""}.
      </p>
      {homeRate <= 0 && rows.length > 0 && (
        <p role="note" className="text-xs text-amber-400">
          Set your home rate above too, so the hours outside these windows are priced.
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <label htmlFor={`${uid}-tz`} className="text-neutral-400">Times are in</label>
        <select id={`${uid}-tz`} value={tz} onChange={(e) => { setTz(e.target.value); setDirty(true); setSaved(false); }} className={`${field} min-w-0 max-w-full`}>
          {zones(tz).map((z) => (
            <option key={z} value={z}>{z}</option>
          ))}
        </select>
      </div>

      {rows.length === 0 && <p className="text-xs text-neutral-500">No off-peak windows yet. Hours outside any window use your home rate.</p>}
      <ol className="space-y-3">
        {rows.map((r, i) => {
          const e = errs[i];
          const id = `${uid}-w${i}`;
          return (
            <li key={i} data-offpeak-window className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3">
              <div className="mb-2 flex items-center justify-between">
                <span className="text-xs font-medium text-neutral-300">Window {i + 1}</span>
                <button type="button" onClick={() => touch(rows.filter((_, j) => j !== i))} aria-label={`Remove window ${i + 1}`} className="text-xs text-neutral-500 hover:text-neutral-300">
                  remove
                </button>
              </div>
              <div className="grid grid-cols-2 gap-x-3 gap-y-2 sm:grid-cols-[auto_auto_auto_auto_1fr]">
                <div className="col-span-2 sm:col-span-1">
                  <label htmlFor={`${id}-days`} className="mb-1 block text-xs text-neutral-400">Days</label>
                  <select id={`${id}-days`} value={r.days} onChange={(ev) => edit(i, { days: ev.target.value as OffpeakDays })} className={`${field} w-full`}>
                    {(Object.keys(DAYS_LABEL) as OffpeakDays[]).map((d) => (
                      <option key={d} value={d}>{DAYS_LABEL[d]}</option>
                    ))}
                  </select>
                </div>
                <div>
                  <label htmlFor={`${id}-start`} className="mb-1 block text-xs text-neutral-400">From</label>
                  <input id={`${id}-start`} type="time" value={r.start} onChange={(ev) => edit(i, { start: ev.target.value })} aria-invalid={!!e.time} className={`${field} w-full`} />
                </div>
                <div>
                  <label htmlFor={`${id}-end`} className="mb-1 block text-xs text-neutral-400">To</label>
                  <input id={`${id}-end`} type="time" value={r.end} onChange={(ev) => edit(i, { end: ev.target.value })} aria-invalid={!!e.time} className={`${field} w-full`} />
                </div>
                <div>
                  <label htmlFor={`${id}-rate`} className="mb-1 block text-xs text-neutral-400">Rate per kWh ({currency})</label>
                  <input
                    id={`${id}-rate`}
                    type="number"
                    inputMode="decimal"
                    step="0.0001"
                    min="0"
                    max={RATE_MAX}
                    value={r.rate}
                    placeholder="0.09"
                    onChange={(ev) => edit(i, { rate: ev.target.value })}
                    aria-invalid={!!e.rate}
                    aria-describedby={e.rate ? `${id}-rate-err` : undefined}
                    className={`${field} w-full tabular-nums`}
                  />
                </div>
                <div className="col-span-2 sm:col-span-1">
                  <label htmlFor={`${id}-label`} className="mb-1 block text-xs text-neutral-400">Name (optional)</label>
                  <input id={`${id}-label`} type="text" maxLength={LABEL_MAX + 20} value={r.label} placeholder="Overnight" onChange={(ev) => edit(i, { label: ev.target.value })} aria-invalid={!!e.label} className={`${field} w-full`} />
                </div>
              </div>
              {r.start && r.end && r.start !== r.end && r.end < r.start && <p className="mt-1.5 text-xs text-neutral-500">Ends the next morning.</p>}
              {e.rate && <p id={`${id}-rate-err`} role="alert" className="mt-1.5 text-xs text-rose-400">{e.rate}</p>}
              {e.time && <p role="alert" className="mt-1.5 text-xs text-rose-400">{e.time}</p>}
              {e.label && <p role="alert" className="mt-1.5 text-xs text-rose-400">{e.label}</p>}
              {e.overlap && <p role="alert" className="mt-1.5 text-xs text-rose-400">{e.overlap}</p>}
              {refusal?.row === i && <p role="alert" className="mt-1.5 text-xs text-rose-400">{refusal.message}</p>}
            </li>
          );
        })}
      </ol>

      {rows.length > 0 && !bad && (
        <div data-offpeak-strips className="space-y-1.5">
          {strips.map((s) => {
            const cov = coverage(rows, s.kind);
            const on = cov.filter(Boolean).length;
            return (
              <div key={s.kind} className="flex items-center gap-2 text-xs text-neutral-400">
                <span className="w-16 shrink-0">{s.label}</span>
                <div
                  role="img"
                  aria-label={`${s.label}: ${on === 0 ? "no off-peak hours" : `${on} off-peak ${on === 1 ? "hour" : "hours"} of 24`}`}
                  className="flex h-3 flex-1 overflow-hidden rounded-sm bg-neutral-800"
                >
                  {cov.map((c, h) => (
                    <span key={h} className={c ? "flex-1 bg-emerald-500/70" : "flex-1"} />
                  ))}
                </div>
              </div>
            );
          })}
          <p className="text-[11px] text-neutral-500">Midnight on the left. Green hours use an off-peak rate; the rest use your home rate.</p>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          disabled={rows.length >= MAX_WINDOWS}
          onClick={() => touch([...rows, newWindow()])}
          className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-200 hover:bg-neutral-800 disabled:opacity-50"
        >
          Add a window
        </button>
        {rows.length >= MAX_WINDOWS && <span className="text-xs text-neutral-500">Up to {MAX_WINDOWS} windows.</span>}
        <button
          type="submit"
          disabled={save.isPending || bad || !dirty}
          className="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
        >
          {save.isPending ? "Saving…" : "Save schedule"}
        </button>
        {saved && !dirty && <span role="status" className="text-xs text-emerald-400">Saved.</span>}
      </div>
      {bad && <p className="text-xs text-rose-400">Fix the marked fields to save.</p>}
      {refusal && (refusal.row === null || refusal.row >= rows.length) && <p role="alert" className="text-xs text-rose-400">{refusal.message}</p>}
      {save.isError && !refusal && <ErrorBox title="Save failed" detail={save.error instanceof Error ? save.error.message : String(save.error)} />}
      {rows.length > 0 && !bad && (
        <p className="text-xs text-neutral-500">
          {rows.map((r) => `${describeWindow(r)} at ${r.rate || "0"} ${currency}/kWh`).join(" · ")}
        </p>
      )}
    </form>
  );
}

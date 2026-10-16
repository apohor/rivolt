import { canStep, periodLabel, stepPeriod, toYmd, type Period, type PeriodKind } from "../lib/period";

// PeriodPicker: calendar scope for the ui_theme_next layer. A segmented
// Week | Month | Year | All | Custom control; calendar periods get arrows to
// step back (never into the future) and a label ("Oct 2026", "Oct 6–12");
// Custom shows two native date inputs.

const KINDS: { key: PeriodKind; label: string }[] = [
  { key: "week", label: "Week" },
  { key: "month", label: "Month" },
  { key: "year", label: "Year" },
  { key: "all", label: "All" },
  { key: "custom", label: "Custom" },
];

function defaultCustom(): Period {
  const to = new Date();
  const from = new Date(to.getFullYear(), to.getMonth(), to.getDate() - 29);
  return { kind: "custom", offset: 0, from: toYmd(from), to: toYmd(to) };
}

export function PeriodPicker({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
  const stepable = canStep(value);
  const label = periodLabel(value);
  const today = toYmd(new Date());
  return (
    <div className="ui-period flex flex-wrap items-center gap-2" role="group" aria-label="Time scope">
      <div className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-xs">
        {KINDS.map((k) => (
          <button
            key={k.key}
            type="button"
            aria-pressed={value.kind === k.key}
            onClick={() => {
              if (k.key === value.kind) return;
              onChange(k.key === "custom" ? (value.from && value.to ? value : defaultCustom()) : { kind: k.key, offset: 0 });
            }}
            className={[
              "rounded-md px-2.5 py-1 transition-colors",
              value.kind === k.key ? "bg-emerald-600 text-white" : "text-neutral-400 hover:text-neutral-200",
            ].join(" ")}
          >
            {k.label}
          </button>
        ))}
      </div>
      {stepable && (
        <div className="inline-flex items-center rounded-lg border border-neutral-800 bg-neutral-900/60 text-xs">
          <button
            type="button"
            aria-label="Previous period"
            onClick={() => onChange(stepPeriod(value, -1))}
            className="rounded-l-lg px-2 py-1 text-neutral-300 hover:bg-neutral-800 hover:text-neutral-100"
          >
            ◀
          </button>
          <span aria-live="polite" className="min-w-24 px-2 text-center tabular-nums text-neutral-100">
            {label}
          </span>
          <button
            type="button"
            aria-label="Next period"
            disabled={value.offset >= 0}
            onClick={() => onChange(stepPeriod(value, 1))}
            className="rounded-r-lg px-2 py-1 text-neutral-300 hover:bg-neutral-800 hover:text-neutral-100 disabled:opacity-30 disabled:hover:bg-transparent"
          >
            ▶
          </button>
        </div>
      )}
      {value.kind === "all" && <span className="text-xs text-neutral-400">{label}</span>}
      {value.kind === "custom" && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-neutral-300">
          <label className="flex items-center gap-1.5">
            <span className="text-neutral-400">From</span>
            <input
              type="date"
              value={value.from ?? ""}
              max={value.to || today}
              onChange={(e) => e.target.value && onChange({ ...value, from: e.target.value })}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-100 focus:border-emerald-500/60 focus:outline-hidden"
            />
          </label>
          <label className="flex items-center gap-1.5">
            <span className="text-neutral-400">To</span>
            <input
              type="date"
              value={value.to ?? ""}
              min={value.from}
              max={today}
              onChange={(e) => e.target.value && onChange({ ...value, to: e.target.value })}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-neutral-100 focus:border-emerald-500/60 focus:outline-hidden"
            />
          </label>
        </div>
      )}
    </div>
  );
}

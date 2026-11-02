import { useEffect, useRef, useState } from "react";
import { canStep, periodLabel, pickerKind, stepPeriod, toYmd, type Period, type PickerKind } from "../lib/period";

// PeriodPicker: calendar scope for the app. Day | Week | Month | 30 days (rolling) | Custom;
// calendar periods get arrows to step back (never into the future) and a
// label ("Today", "Oct 24", "Oct 2026", "Oct 6–12"). On desktop the segmented
// control is inline; on a phone it collapses to a compact bar (label with
// arrows) and a tap on the label opens the same choices in a panel that sits in the
// flow under the bar (it used to float over the page and hid the first tab, "Charges" on Activity).
// Custom shows two native date inputs and two shortcuts, "This year" and
// "All time" (a saved Year or All still works; the picker shows it as Custom).
// The iPhone app's picker has the same choices and labels.

const KINDS: { key: PickerKind; label: string }[] = [
  { key: "day", label: "Day" },
  { key: "week", label: "Week" },
  { key: "month", label: "Month" },
  { key: "last30", label: "30 days" },
  { key: "custom", label: "Custom" },
];

function defaultCustom(): Period {
  const to = new Date();
  const from = new Date(to.getFullYear(), to.getMonth(), to.getDate() - 29);
  return { kind: "custom", offset: 0, from: toYmd(from), to: toYmd(to) };
}

function Segmented({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
  const shown = pickerKind(value);
  return (
    <div className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-xs">
      {KINDS.map((k) => (
        <button
          key={k.key}
          type="button"
          aria-pressed={shown === k.key}
          onClick={() => {
            if (k.key === shown) return;
            onChange(k.key === "custom" ? (value.from && value.to ? { ...value, kind: "custom", offset: 0 } : defaultCustom()) : { kind: k.key, offset: 0 });
          }}
          className={[
            "rounded-md px-2.5 py-1 transition-colors",
            shown === k.key ? "bg-emerald-600 text-white" : "text-neutral-400 hover:text-neutral-200",
          ].join(" ")}
        >
          {k.label}
        </button>
      ))}
    </div>
  );
}

// "This year" and "All time", one tap each, inside Custom. The one in use is pressed.
function Shortcuts({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
  const chip = (on: boolean) =>
    ["rounded-md border px-2 py-1 transition-colors", on ? "border-emerald-600 bg-emerald-600/20 text-emerald-200" : "border-neutral-700 text-neutral-300 hover:text-neutral-100"].join(" ");
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <button type="button" aria-pressed={value.kind === "year"} className={chip(value.kind === "year")} onClick={() => onChange({ kind: "year", offset: 0 })}>
        This year
      </button>
      <button type="button" aria-pressed={value.kind === "all"} className={chip(value.kind === "all")} onClick={() => onChange({ kind: "all", offset: 0 })}>
        All time
      </button>
      {(value.kind === "year" || value.kind === "all") && (
        <button type="button" className={chip(false)} onClick={() => onChange(defaultCustom())}>
          Pick dates
        </button>
      )}
    </div>
  );
}

// Custom's panel: the date inputs (when dates are in use) and the shortcuts.
function CustomPanel({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      {value.kind === "custom" && <CustomInputs value={value} onChange={onChange} />}
      <Shortcuts value={value} onChange={onChange} />
    </div>
  );
}

function CustomInputs({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
  const today = toYmd(new Date());
  return (
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
  );
}

export function PeriodPicker({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
  const stepable = canStep(value);
  const label = periodLabel(value);
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement | null>(null);
  // Close the phone panel on an outside tap or Escape.
  useEffect(() => {
    if (!open) return;
    const away = (e: Event) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);

  const arrow = "px-2 py-1 text-neutral-300 hover:bg-neutral-800 hover:text-neutral-100 disabled:opacity-30 disabled:hover:bg-transparent";
  return (
    <div ref={root} className="ui-period relative flex flex-wrap items-center justify-end gap-2" role="group" aria-label="Time scope">
      <div className="hidden md:block">
        <Segmented value={value} onChange={onChange} />
      </div>
      <div className="ui-period-bar inline-flex max-w-full items-center rounded-lg border border-neutral-800 bg-neutral-900/60 text-xs">
        {stepable && (
          <button type="button" aria-label="Previous period" onClick={() => onChange(stepPeriod(value, -1))} className={`rounded-l-lg ${arrow}`}>
            ◀
          </button>
        )}
        {/* Desktop: a plain label. Phone: a button that opens the choices. */}
        <span aria-live="polite" className="hidden min-w-24 px-2 text-center tabular-nums text-neutral-100 md:inline">
          {label}
        </span>
        <button
          type="button"
          aria-expanded={open}
          aria-haspopup="true"
          aria-label={`Time scope: ${label}. Change`}
          onClick={() => setOpen((v) => !v)}
          className="min-w-16 px-2 py-1 text-center tabular-nums text-neutral-100 md:hidden"
        >
          {label}
        </button>
        {stepable && (
          <button type="button" aria-label="Next period" disabled={value.offset >= 0} onClick={() => onChange(stepPeriod(value, 1))} className={`rounded-r-lg ${arrow}`}>
            ▶
          </button>
        )}
      </div>
      {pickerKind(value) === "custom" && (
        <div className="hidden md:block">
          <CustomPanel value={value} onChange={onChange} />
        </div>
      )}
      {open && (
        <div data-period-panel className="mt-1 w-full basis-full space-y-3 rounded-xl border border-neutral-700 bg-neutral-900 p-3 md:hidden">
          <Segmented value={value} onChange={onChange} />
          {pickerKind(value) === "custom" && <CustomPanel value={value} onChange={onChange} />}
        </div>
      )}
    </div>
  );
}

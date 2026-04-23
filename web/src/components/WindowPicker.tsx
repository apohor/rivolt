import { WINDOW_OPTIONS, type WindowKey } from "../lib/analytics";

// Pill-style segmented control for selecting the time window (7d / 30d /
// 90d / 365d / all). Lives in a shared component so Overview, Drives,
// and Charges stay visually consistent.
export function WindowPicker({
  value,
  onChange,
}: {
  value: WindowKey;
  onChange: (v: WindowKey) => void;
}) {
  return (
    <div className="inline-flex rounded-lg border border-neutral-800 bg-neutral-900/60 p-0.5 text-xs">
      {WINDOW_OPTIONS.map((opt) => (
        <button
          key={opt.key}
          type="button"
          onClick={() => onChange(opt.key)}
          className={[
            "rounded-md px-2.5 py-1 transition-colors",
            value === opt.key
              ? "bg-emerald-600 text-white"
              : "text-neutral-400 hover:text-neutral-200",
          ].join(" ")}
        >
          {opt.label}
        </button>
      ))}
    </div>
  );
}

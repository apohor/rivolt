import type { Vehicle } from "../lib/api";

// VehiclePicker is the multi-vehicle-aware replacement for the
// old `vehicles[0]` assumption on single-vehicle pages.
//
// Zero UI when the user has 0 or 1 cars — rendering a
// one-option dropdown would be noise for the 90% case (single
// operator, single Rivian). Only when there's something to
// pick between does the picker appear, styled as a compact
// segmented pill that fits in the hero footer next to the
// window-picker.
//
// Keyboard: the <select> is native, so ↑/↓ / type-to-jump /
// Escape all work out of the box. Screen readers announce the
// label via aria-label.
export function VehiclePicker({
  vehicles,
  selectedID,
  onChange,
  label = "Vehicle",
  className = "",
}: {
  vehicles: Vehicle[];
  selectedID: string | undefined;
  onChange: (id: string) => void;
  label?: string;
  className?: string;
}) {
  if (!vehicles || vehicles.length <= 1) return null;

  return (
    <label
      className={
        "inline-flex items-center gap-1.5 rounded-md border border-neutral-800 bg-neutral-950/60 px-2 py-1 text-[11px] text-neutral-300 " +
        className
      }
    >
      <span className="uppercase tracking-wide text-neutral-500">{label}</span>
      <select
        aria-label={label}
        value={selectedID ?? ""}
        onChange={(e) => onChange(e.target.value)}
        className="cursor-pointer border-none bg-transparent text-[11px] text-neutral-200 focus:outline-none focus:ring-1 focus:ring-emerald-500"
      >
        {vehicles.map((v) => (
          <option key={v.id} value={v.id}>
            {displayName(v)}
          </option>
        ))}
      </select>
    </label>
  );
}

// displayName prefers the user-set name, then falls back to
// model + last-6-of-VIN so two R1S's on the same account are
// still distinguishable before they've been renamed.
function displayName(v: Vehicle): string {
  if (v.name && v.name.trim()) return v.name;
  const parts: string[] = [];
  if (v.model) parts.push(v.model);
  if (v.vin) parts.push(v.vin.slice(-6));
  return parts.length > 0 ? parts.join(" · ") : v.id.slice(0, 8);
}

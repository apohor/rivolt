import { useId } from "react";
import { type RoutePrefs } from "./routePrefs";

// "Avoid" tolls, ferries and highways. Copy is growth's (planner-copy.md §7):
// the helper says these apply to every day and are saved with the trip, and
// that avoiding highways usually costs time and may add charging stops.

const OPTIONS: { key: keyof RoutePrefs; label: string }[] = [
  { key: "avoidTolls", label: "Tolls" },
  { key: "avoidFerries", label: "Ferries" },
  { key: "avoidHighways", label: "Highways" },
];

export function AvoidToggles({ value, onChange }: { value: RoutePrefs; onChange: (next: RoutePrefs) => void }) {
  const helpId = useId();
  return (
    <fieldset className="flex flex-col gap-2 text-sm" aria-describedby={helpId} data-avoid-toggles>
      <legend className="text-neutral-400">Avoid</legend>
      <div className="flex flex-wrap gap-2">
        {OPTIONS.map(({ key, label }) => (
          <label
            key={key}
            className={`inline-flex cursor-pointer items-center gap-2 rounded-full border px-3 py-1.5 text-xs transition-colors ${
              value[key]
                ? "border-emerald-600/60 bg-emerald-600/15 text-emerald-200"
                : "border-neutral-700 text-neutral-300 hover:border-neutral-500"
            }`}
          >
            <input
              type="checkbox"
              className="h-3.5 w-3.5 accent-emerald-600"
              checked={value[key]}
              onChange={(e) => onChange({ ...value, [key]: e.target.checked })}
            />
            {label}
          </label>
        ))}
      </div>
      <p id={helpId} className="text-xs text-neutral-500">
        Applies to every day of this trip and is saved with it. Avoiding highways usually adds time and may add charging stops.
      </p>
    </fieldset>
  );
}

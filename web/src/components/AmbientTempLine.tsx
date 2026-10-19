import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { AMBIENT_TEMP_TEXT, useAmbientTemp } from "../lib/ambientTemp";
import { formatTemperature, usePreferences } from "../lib/preferences";

const HIDE_KEY = "rivolt.ambientTemp.offerHidden";
const hidden = (): boolean => {
  try {
    return window.localStorage.getItem(HIDE_KEY) === "1";
  } catch {
    return false;
  }
};

// Under Now's details: the outside temperature when the user turned it on, or, while it is off, a quiet offer to
// turn it on that shows the full text first. Not shown in the demo or where the server has no such setting.
export function AmbientTempLine({ ambient, vehicleID }: { ambient?: { temp_c: number } | null; vehicleID?: string }) {
  const amb = useAmbientTemp();
  const { temperatureUnit } = usePreferences();
  const [open, setOpen] = useState(false);
  const [gone, setGone] = useState(hidden);
  const [err, setErr] = useState(false);
  if (amb.enabled === undefined || amb.enabled === null) return null;
  if (amb.enabled) {
    return ambient ? (
      <>
        <p data-ambient-temp className="mt-2 text-xs text-neutral-400">
          Outside {formatTemperature(ambient.temp_c, temperatureUnit)} <span className="text-neutral-500">· estimate from Open-Meteo</span>
        </p>
        {vehicleID && <ColdRangeSentence vehicleID={vehicleID} tempC={ambient.temp_c} />}
      </>
    ) : null;
  }
  if (gone) return null;
  const hide = () => {
    setGone(true);
    try {
      window.localStorage.setItem(HIDE_KEY, "1");
    } catch {
      /* hides for this visit only */
    }
  };
  return (
    <div data-ambient-offer className="mt-2 text-xs text-neutral-400">
      {!open ? (
        <button type="button" onClick={() => setOpen(true)} className="text-neutral-300 underline underline-offset-2 hover:text-emerald-300">
          Show outside temperature
        </button>
      ) : (
        <div className="space-y-2">
          <p>{AMBIENT_TEMP_TEXT}</p>
          <div className="flex gap-2">
            <button
              type="button"
              disabled={amb.saving}
              onClick={() => {
                setErr(false);
                amb.set(true).catch(() => setErr(true));
              }}
              className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50"
            >
              Turn on
            </button>
            <button type="button" onClick={hide} className="px-2 text-neutral-400 hover:text-neutral-200">
              Not now
            </button>
          </div>
          {err && <p role="alert" className="text-rose-400">Couldn&apos;t turn that on. Try again in a moment.</p>}
        </div>
      )}
    </div>
  );
}

// With the live reading on, one sentence from the user's own drives near this temperature. Nothing when there isn't
// enough history (the Truck tab says what is missing) and never a comparison with anyone else.
function ColdRangeSentence({ vehicleID, tempC }: { vehicleID: string; tempC: number }) {
  const { distanceUnit, temperatureUnit } = usePreferences();
  const units = distanceUnit === "km" ? "metric" : "imperial";
  const rounded = Math.round(tempC);
  const q = useQuery({
    queryKey: ["cold-range", "now", vehicleID, units, rounded],
    queryFn: () => backend.coldRange(vehicleID, rounded, [], units),
    staleTime: 30 * 60_000,
    retry: 0,
  });
  const d = q.data;
  if (!d || d.reason || d.efficiency_low == null || d.efficiency_high == null) return null;
  const band = (lo: number, hi: number, digits: number, unit: string) => {
    const a = lo.toFixed(digits);
    const b = hi.toFixed(digits);
    return a === b ? `${a} ${unit}` : `${a}–${b} ${unit}`;
  };
  const now = `It's ${formatTemperature(tempC, temperatureUnit)} now`;
  return (
    <p data-cold-range-now className="mt-1 text-xs text-neutral-300">
      {d.range_low != null && d.range_high != null
        ? `${now}: about ${band(d.range_low, d.range_high, 0, d.distance_unit)} on a full charge, from your own drives (estimate).`
        : `${now}: your drives near this temperature average about ${band(d.efficiency_low, d.efficiency_high, 1, d.efficiency_unit)} (estimate).`}
    </p>
  );
}

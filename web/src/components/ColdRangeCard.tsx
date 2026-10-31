import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ProLimitNotice } from "./ProLimitNotice";
import { proLimitOf, retryUnlessLimit } from "../lib/proLimit";
import { backend, type ColdRange } from "../lib/api";
import { formatTemperature, usePreferences, type TemperatureUnit } from "../lib/preferences";
import { Card } from "./ui";

// The five temperatures offered, in °C (the server works in °C): 0, 20, 40, 60, 80 °F, or round °C steps.
const GRID_F = [-17.8, -6.7, 4.4, 15.6, 26.7];
const GRID_C = [-20, -10, 0, 10, 20];
const near = (a: number, b: number) => Math.abs(a - b) < 0.3;
// A chip's label: whole degrees, never "-0".
const chip = (c: number, u: TemperatureUnit): string => (u === "f" ? `${Math.round(c * 1.8 + 32)} °F` : `${Math.round(c)} °C`);
const note = "text-sm text-neutral-400";

// "Range at different temperatures" (Truck tab): the user's own efficiency and range near a temperature, as a band and
// labelled an estimate. The temperature is only the question: chips pick it, and a chip with too little history of your own
// is greyed out (the demo shows it with sample figures). Nothing is drawn for a temperature the truck has no history for, and never a comparison with anyone else.
export function ColdRangeCard({ vehicleID }: { vehicleID: string }) {
  const { distanceUnit, temperatureUnit } = usePreferences();
  const units = distanceUnit === "km" ? "metric" : "imperial";
  const grid = temperatureUnit === "f" ? GRID_F : GRID_C;
  const [picked, setPicked] = useState<number | null>(null);
  const [auto, setAuto] = useState<number | null>(null);
  // Until a chip is picked: the coldest one with enough history (the winter question), else the middle chip.
  const temp = picked ?? auto ?? grid[2];
  const q = useQuery({
    queryKey: ["cold-range", vehicleID, units, temp, temperatureUnit],
    queryFn: async () => {
      const r = await backend.coldRange(vehicleID, temp, grid, units);
      if (r && picked === null && auto === null && r.available.length > 0) setAuto(Math.min(...r.available));
      return r;
    },
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
    retry: retryUnlessLimit(1),
  });
  if (q.data === null) return null;
  const limit = proLimitOf(q.error);
  if (limit) {
    return (
      <Card title="Range at different temperatures" span="full">
        <ProLimitNotice limit={limit} />
      </Card>
    );
  }
  const d = q.data;
  return (
    <Card title="Range at different temperatures" span="full">
      <div data-cold-range className="space-y-3">
        {q.isPending && <div className="h-16 animate-pulse rounded-sm bg-neutral-900/50" />}
        {q.isError && (
          <p role="status" className={note}>
            Couldn&apos;t load this.{" "}
            <button type="button" onClick={() => void q.refetch()} className="text-emerald-300 underline underline-offset-2">
              Try again
            </button>
          </p>
        )}
        {d && d.reason === "not_tracked" && <p data-cold-state="untracked" className={note}>Can&apos;t be tracked on this truck yet.</p>}
        {d && d.reason !== "not_tracked" && (
          <>
            <div role="group" aria-label="Temperature" className="flex flex-wrap gap-2">
              {grid.map((t) => {
                const off = d.available.length > 0 || d.reason === "not_enough_data" ? !d.available.some((a) => near(a, t)) : false;
                const on = near(t, temp);
                return (
                  <button
                    key={t}
                    type="button"
                    disabled={off && !on}
                    aria-pressed={on}
                    onClick={() => setPicked(t)}
                    className={
                      "rounded-md border px-3 py-1.5 text-sm tabular-nums " +
                      (on ? "border-emerald-500/60 bg-emerald-600/20 text-emerald-300" : "border-neutral-700 text-neutral-200 hover:border-neutral-500 disabled:opacity-40 disabled:hover:border-neutral-700")
                    }
                  >
                    {chip(t, temperatureUnit)}
                  </button>
                );
              })}
            </div>
            <Result d={d} temp={temp} unit={temperatureUnit} />
          </>
        )}
      </div>
    </Card>
  );
}

function Result({ d, temp, unit }: { d: ColdRange; temp: number; unit: TemperatureUnit }) {
  const at = chip(temp, unit);
  if (d.reason === "not_enough_data" || d.efficiency_low == null || d.efficiency_high == null) {
    return (
      <p data-cold-state="sparse" className={note}>
        Not enough drives near {at} yet. It needs about {d.min_drives} drives and {d.min_miles} {d.distance_unit} near that temperature.
      </p>
    );
  }
  // One value when the band's ends round to the same number, never "283–283".
  const band = (lo: number, hi: number, digits: number, unit: string) => {
    const a = lo.toFixed(digits);
    const b = hi.toFixed(digits);
    return a === b ? `${a} ${unit}` : `${a}–${b} ${unit}`;
  };
  const eff = band(d.efficiency_low, d.efficiency_high, 1, d.efficiency_unit);
  return (
    <div data-cold-state="ok" className="space-y-1">
      <p className="text-neutral-100">
        {d.range_low != null && d.range_high != null ? (
          <>
            Around {at}, a full charge usually gives about <span className="font-semibold tabular-nums">{band(d.range_low, d.range_high, 0, d.distance_unit)}</span>, from your own drives (estimate).
          </>
        ) : (
          <>
            Around {at}, your drives average about <span className="font-semibold tabular-nums">{eff}</span> (estimate).
          </>
        )}
      </p>
      {d.range_low != null && <p className="text-xs text-neutral-400">That is {eff}.</p>}
      {d.based_on && (
        <p className="text-xs text-neutral-500">
          Based on {d.based_on.drives} of your drives ({Math.round(d.based_on.miles)} {d.distance_unit}) between {formatTemperature(d.based_on.temp_lo_c, unit)} and {formatTemperature(d.based_on.temp_hi_c, unit)}. The temperature comes from weather data, not from your truck.
        </p>
      )}
    </div>
  );
}

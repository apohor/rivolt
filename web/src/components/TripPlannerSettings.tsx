// The Trip planner tab's map cards: how chargers show on the planner map, and what the map starts with. Each choice saves as soon as it is made.
import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { appPath } from "../lib/demo";
import type { Corridor, LayerKindName, MinFastKW, StartExcludeMi } from "../lib/api";
import { radarAvailable, usePlannerPrefs, useSavePlannerPrefs } from "../lib/plannerPrefs";
import { Card, Toggle } from "./ui";

const select = "rounded-md border border-neutral-700 bg-neutral-900 px-3 py-2 text-neutral-100 focus:border-neutral-500 focus:outline-hidden";

export const CORRIDOR_CHOICES: { id: Corridor; label: string }[] = [
  { id: "close", label: "Close" },
  { id: "normal", label: "Normal" },
  { id: "wide", label: "Wide" },
];
// The distances behind the names, in miles (fast / Level 2 / hotels and campgrounds): kept next to the server's presets.
export const CORRIDOR_MILES: Record<Corridor, string> = {
  close: "2 mi for fast chargers, 1 mi for Level 2, 5 mi for hotels and campgrounds",
  normal: "5 mi for fast chargers, 3 mi for Level 2, 10 mi for hotels and campgrounds",
  wide: "10 mi for fast chargers, 5 mi for Level 2, 20 mi for hotels and campgrounds",
};
export const START_EXCLUDE_CHOICES: { id: StartExcludeMi; label: string }[] = [
  { id: 0, label: "Off" },
  { id: 25, label: "First 25 mi" },
  { id: 50, label: "First 50 mi" },
];
export const MIN_FAST_CHOICES: { id: MinFastKW; label: string }[] = [
  { id: 0, label: "Any" },
  { id: 50, label: "50 kW" },
  { id: 150, label: "150 kW" },
  { id: 250, label: "250 kW" },
];
export const LAYER_CHOICES: { id: LayerKindName; label: string }[] = [
  { id: "dcfc", label: "Fast chargers" },
  { id: "l2", label: "Level 2" },
  { id: "hotels", label: "Hotels" },
  { id: "camp", label: "Camping" },
];

function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-neutral-400">{label}</span>
      {children}
      {hint && <span className="text-xs text-neutral-500">{hint}</span>}
    </div>
  );
}

function SaveState({ save }: { save: ReturnType<typeof useSavePlannerPrefs> }) {
  if (save.isError) return <span role="alert" className="text-xs text-rose-400">Not saved: {String(save.error)}</span>;
  if (save.isSuccess) return <span role="status" className="text-xs text-emerald-400">Saved.</span>;
  return null;
}

export function ChargersOnMapCard() {
  const { prefs, settled } = usePlannerPrefs();
  const save = useSavePlannerPrefs();
  const toggleLayer = (k: LayerKindName) => {
    const on = prefs.layers_on.includes(k) ? prefs.layers_on.filter((x) => x !== k) : [...prefs.layers_on, k];
    save.mutate({ layers_on: LAYER_CHOICES.map((c) => c.id).filter((id) => on.includes(id)) });
  };
  return (
    <Card title="Chargers on the map" id="charger-map">
      <div className="space-y-4 text-sm" aria-busy={!settled}>
        <p className="text-xs text-neutral-500">These only change the extra pins on the planner map. The stops Rivian plans always show.</p>
        <Row label="How far from the route" hint={CORRIDOR_MILES[prefs.corridor]}>
          <select aria-label="How far from the route" value={prefs.corridor} disabled={!settled} onChange={(e) => save.mutate({ corridor: e.target.value as Corridor })} className={select}>
            {CORRIDOR_CHOICES.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
          </select>
        </Row>
        <Row label="Hide chargers near the start" hint="Turn on to hide chargers in the first miles of the first day, if you usually start charged. Rivian's planned stops always show.">
          <select aria-label="Hide chargers near the start" value={prefs.start_exclude_mi} disabled={!settled} onChange={(e) => save.mutate({ start_exclude_mi: Number(e.target.value) as StartExcludeMi })} className={select}>
            {START_EXCLUDE_CHOICES.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
          </select>
        </Row>
        <Row label="Minimum fast-charger power" hint="Hides fast chargers rated below this. A charger with no listed power stays.">
          <select aria-label="Minimum fast-charger power" value={prefs.min_fast_kw} disabled={!settled} onChange={(e) => save.mutate({ min_fast_kw: Number(e.target.value) as MinFastKW })} className={select}>
            {MIN_FAST_CHOICES.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
          </select>
        </Row>
        <Row label="Layers that start on" hint="You can still switch them on and off on the map.">
          <div className="flex flex-wrap gap-3">
            {LAYER_CHOICES.map((c) => (
              <label key={c.id} className="flex items-center gap-2 text-neutral-200">
                <input type="checkbox" checked={prefs.layers_on.includes(c.id)} disabled={!settled} onChange={() => toggleLayer(c.id)} />
                {c.label}
              </label>
            ))}
          </div>
        </Row>
        <Row label="Preferred networks only" hint="Fast pins only from the networks you prefer. Choose them under Charging networks in Settings.">
          <Toggle id="planner-preferred-only" checked={prefs.preferred_only} disabled={!settled} onChange={(v) => save.mutate({ preferred_only: v })} />
          <Link to={appPath("/settings?tab=charging#networks")} className="text-xs text-emerald-400 hover:text-emerald-300">Choose preferred networks (Charging networks)</Link>
        </Row>
        <SaveState save={save} />
      </div>
    </Card>
  );
}

export function MapDefaultsCard() {
  const { prefs, settled } = usePlannerPrefs();
  const save = useSavePlannerPrefs();
  return (
    <Card title="Map" id="map-defaults">
      <div className="space-y-4 text-sm" aria-busy={!settled}>
        <Row label="Weather on each leg" hint="The temperature and wind on each leg, on by default.">
          <Toggle id="planner-weather-on" checked={prefs.weather_on} disabled={!settled} onChange={(v) => save.mutate({ weather_on: v })} />
        </Row>
        {radarAvailable() && (
          <Row label="Radar" hint="Live NOAA radar under the route. Shown only where this server has radar.">
            <Toggle id="planner-radar-on" checked={prefs.radar_on} disabled={!settled} onChange={(v) => save.mutate({ radar_on: v })} />
          </Row>
        )}
        <SaveState save={save} />
      </div>
    </Card>
  );
}

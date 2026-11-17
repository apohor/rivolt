import { clock, type NowView } from "../lib/nowState";
import { formatMoney } from "../lib/ledger";
import { formatDriveMode } from "../lib/driveMode";
import { formatTemperature, type DistanceUnit, type TemperatureUnit } from "../lib/preferences";
import { ParkedMap } from "../map/ParkedMap";
import { inDemo } from "../lib/demo";

// The state slot of the Now hero (design_preview "now_slot"): under the gauge, in place of the vehicle picture and the rows,
// the figures of what the truck is doing. Charging: power and the charger, when it reaches the limit, then the session. Driving:
// speed, gear and mode, then the drive so far. Only values that exist are shown; nothing is a placeholder.

export type SlotSession = { range_added_km: number; total_charged_energy_kwh: number; time_elapsed_seconds: number; kilometers_charged_per_hour: number };
export type SlotCharge = { started_at: string; cost?: number | null; currency?: string; cost_source?: string; cost_is_estimate?: boolean };
export type SlotDrive = { tripMi: number; elapsed_sec: number; energy_used_kwh: number; soc_used_pct: number };
export type SlotInput = {
  view: NowView;
  nowMs: number;
  unit: DistanceUnit;
  currency: string;
  gear?: string;
  mode?: string;
  session?: SlotSession | null;
  charge?: SlotCharge | null;
  drive?: SlotDrive | null;
  fix?: SlotFix | null;
  weather?: SlotWeather | null;
  tempUnit?: TemperatureUnit;
};
export type SlotFix = { lat: number; lon: number };
export type SlotWeather = { temp_c: number; condition?: string; wind_kph?: number; rain_next3h_mm?: number };

const KM_PER_MI = 1.609344;
type Stat = { k: string; v: string; note?: string };

const COST_NOTE: Record<string, string> = {
  rivian: "billed by Rivian",
  user: "your price",
  home_rate: "at your home rate",
  network_estimate: "network rate",
  observed_estimate: "from similar sessions",
};
const CHARGER: Record<string, string> = { home_l2: "Home charger", dcfc: "Fast charger", away_ac: "Public charger" };

// The mock's wording: "3 h 26 min", "45 min" (no tilde; our own estimate says so after it).
function timeLeft(hours: number): string {
  const m = Math.max(1, Math.round(hours * 60));
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} min`;
}

function duration(sec: number): string {
  const m = Math.max(0, Math.round(sec / 60));
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")}`;
}

export function slotStats(i: SlotInput): Stat[] {
  const { view: v, unit } = i;
  const dist = (mi: number) => `${(unit === "km" ? mi * KM_PER_MI : mi).toFixed(unit === "km" || mi < 100 ? 1 : 0)} ${unit}`;
  const out: Stat[] = [];
  if (v.kind === "charging") {
    const s = i.session;
    // The truck's own session figure when it reports one; the SoC-based estimate (≈) otherwise.
    if (s && s.total_charged_energy_kwh > 0) out.push({ k: "Added", v: `+${s.total_charged_energy_kwh.toFixed(1)} kWh` });
    else if (v.energyKwh != null && v.energyKwh > 0) out.push({ k: "Added", v: `≈ +${v.energyKwh.toFixed(1)} kWh` });
    if (s && s.range_added_km > 0) out.push({ k: "Range added", v: `+${dist(s.range_added_km / KM_PER_MI)}` });
    if (s && s.kilometers_charged_per_hour > 0) out.push({ k: "Rate", v: `${dist(s.kilometers_charged_per_hour / KM_PER_MI)}/h` });
    const c = i.charge;
    const startedMs = c ? Date.parse(c.started_at) : NaN;
    // The truck's own elapsed time; the recorded session's start otherwise.
    if (s && s.time_elapsed_seconds > 0) out.push({ k: "Elapsed", v: duration(s.time_elapsed_seconds) });
    else if (Number.isFinite(startedMs) && i.nowMs > startedMs) out.push({ k: "Elapsed", v: duration((i.nowMs - startedMs) / 1000) });
    if (c && c.cost != null) {
      const est = c.cost_is_estimate !== false;
      out.push({ k: "Cost so far", v: `${est ? "≈ " : ""}${formatMoney(c.cost, c.currency || i.currency)}`, note: (c.cost_source && COST_NOTE[c.cost_source]) || (est ? "estimate" : undefined) });
    }
    if (Number.isFinite(startedMs)) out.push({ k: "Plugged in", v: clock(startedMs) });
  } else if (v.kind === "driving" && i.drive) {
    const d = i.drive;
    if (d.tripMi > 0) out.push({ k: "Distance", v: dist(d.tripMi) });
    if (d.elapsed_sec > 0) out.push({ k: "Time", v: duration(d.elapsed_sec) });
    // Efficiency is the distance shown over the energy shown, so the four figures describe one drive; not until it can't be absurd.
    if (d.tripMi >= 1 && d.soc_used_pct >= 1 && d.energy_used_kwh > 0) {
      const e = d.tripMi / d.energy_used_kwh;
      out.push({ k: "Efficiency", v: unit === "km" ? `${(e * KM_PER_MI).toFixed(1)} km/kWh` : `${e.toFixed(1)} mi/kWh` });
    }
    if (d.energy_used_kwh > 0) out.push({ k: "Energy used", v: `${d.energy_used_kwh.toFixed(1)} kWh` });
  }
  return out;
}

// Whether the slot has its big figure (or, parked, a map to draw); when not, the picture stays.
export function slotAvailable(v: NowView, fix: SlotFix | null = null): boolean {
  return (v.kind === "charging" && v.powerKw != null) || (v.kind === "driving" && v.speedMph != null) || ((v.kind === "parked" || v.kind === "plugged") && fix != null);
}

const CONDITIONS: Record<string, string> = {
  clear: "Clear",
  partly_cloudy: "Partly cloudy",
  cloudy: "Cloudy",
  fog: "Fog",
  drizzle: "Drizzle",
  rain: "Rain",
  snow: "Snow",
  thunder: "Thunderstorms",
};

// The weather lines of the parked slot: only what the server sent. The 3 h amount is rain or snow by the current condition.
export function weatherLines(w: SlotWeather, tempUnit: TemperatureUnit, unit: DistanceUnit): { temp: string; lines: string[] } {
  const lines: string[] = [];
  const cond = w.condition ? CONDITIONS[w.condition] : undefined;
  if (cond) lines.push(cond);
  if (w.wind_kph != null) lines.push(`Wind ${unit === "km" ? `${Math.round(w.wind_kph)} km/h` : `${Math.round(w.wind_kph / KM_PER_MI)} mph`}`);
  if (w.rain_next3h_mm != null) {
    const what = w.condition === "snow" ? "snow" : "rain";
    const mm = w.rain_next3h_mm;
    if (mm < 0.05) lines.push(`No ${what} next 3 h`);
    else lines.push(`${what[0].toUpperCase()}${what.slice(1)} next 3 h · ${unit === "km" ? `${+mm.toFixed(1)} mm` : `${Math.max(0.1, +(mm / 25.4).toFixed(1))} in`}`);
  }
  return { temp: formatTemperature(w.temp_c, tempUnit), lines };
}

function Chip({ children, tone, testid }: { children: React.ReactNode; tone: "green" | "grey"; testid: string }) {
  return (
    <span data-testid={testid} className={`ml-2 inline-block rounded-md px-1.5 py-0.5 align-middle text-xs font-medium ${tone === "green" ? "bg-emerald-500/15 text-emerald-300" : "bg-neutral-800 text-neutral-300"}`}>
      {children}
    </span>
  );
}

const ROOT = "pointer-events-none relative mt-4 w-full min-w-0 border-t border-neutral-800 pt-4 min-[900px]:col-start-2 min-[900px]:row-span-2 min-[900px]:row-start-1 min-[900px]:mt-0 min-[900px]:border-t-0 min-[900px]:pt-0";

// Parked or plugged in: where the truck is on the map and, when the server sent it, the weather there.
function ParkedSlot({ fix, weather, tempUnit, unit, kind }: { fix: SlotFix; weather: SlotWeather | null; tempUnit: TemperatureUnit; unit: DistanceUnit; kind: string }) {
  const w = weather ? weatherLines(weather, tempUnit, unit) : null;
  return (
    <div data-testid="now-slot" data-kind={kind} className={ROOT}>
      <div className="flex flex-col gap-3 min-[900px]:flex-row">
        <div className="min-w-0 flex-1">
          <ParkedMap lat={fix.lat} lon={fix.lon} />
          {inDemo() && <p className="mt-1 text-xs text-neutral-500">Sample map, not a real location</p>}
        </div>
        {w && (
          <div data-testid="now-slot-weather" className="shrink-0 min-[900px]:w-40">
            <p className="ui-label">Weather</p>
            <p className="text-3xl font-semibold leading-none tabular-nums text-neutral-100" data-testid="now-slot-temp">{w.temp}</p>
            {w.lines.map((l) => (
              <p key={l} className="mt-1 text-sm text-neutral-400">{l}</p>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// Null when the state has no slot, so the picture stays.
export default function NowSlot(i: SlotInput) {
  const { view, nowMs, unit } = i;
  if (!slotAvailable(view, i.fix ?? null)) return null;
  if (view.kind === "parked" || view.kind === "plugged") return <ParkedSlot fix={i.fix as SlotFix} weather={i.weather ?? null} tempUnit={i.tempUnit ?? "c"} unit={unit} kind={view.kind} />;
  const stats = slotStats(i);
  let big = "";
  let bigUnit = "";
  let sub: string | null = null;
  let small: string | null = null;
  const chips: React.ReactNode[] = [];
  if (view.kind === "charging" && view.powerKw != null) {
    big = view.powerKw.toFixed(view.powerKw < 10 ? 1 : 0);
    bigUnit = "kW";
    const label = view.chargeFlavor ? CHARGER[view.chargeFlavor] : null;
    if (label) chips.push(<Chip key="c" tone="green" testid="now-slot-charger">{label}</Chip>);
    if (view.etaHours != null) {
      const when = clock(nowMs + view.etaHours * 3_600_000);
      sub = view.limitPct != null && view.limitPct < 100 ? `Reaches ${Math.round(view.limitPct)}% at ${when}` : `Full at ${when}`;
      small = `${timeLeft(view.etaHours)} left${view.etaSource === "truck" ? "" : " (est.)"}${view.limitPct != null ? ` · target ${Math.round(view.limitPct)}%` : ""}`;
    }
  } else if (view.kind === "driving" && view.speedMph != null) {
    const sp = unit === "km" ? view.speedMph * KM_PER_MI : view.speedMph;
    big = String(Math.round(sp));
    bigUnit = unit === "km" ? "km/h" : "mph";
    const gear = (i.gear || "").toUpperCase();
    if (gear) chips.push(<Chip key="g" tone="grey" testid="now-slot-gear">{gear}</Chip>);
    const mode = i.mode ? formatDriveMode(i.mode) : null;
    if (mode) chips.push(<Chip key="m" tone="green" testid="now-slot-mode">{mode}</Chip>);
    sub = stats.length > 0 ? "This drive so far" : null;
  }
  return (
    <div data-testid="now-slot" data-kind={view.kind} className={ROOT}>
      <p className="text-4xl font-semibold leading-none tabular-nums text-neutral-100" data-testid="now-slot-big">
        {big}
        <span className="ml-1 text-base font-normal text-neutral-400">{bigUnit}</span>
        {chips}
      </p>
      {sub && <p className="mt-2 text-sm font-medium text-neutral-200" data-testid="now-slot-sub">{sub}</p>}
      {small && <p className="text-xs text-neutral-400" data-testid="now-slot-small">{small}</p>}
      {stats.length > 0 && (
        <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-3 min-[900px]:grid-cols-3">
          {stats.map((s) => (
            <div key={s.k} data-testid={`now-slot-${s.k.toLowerCase().replace(/ /g, "-")}`}>
              <p className="ui-label">{s.k}</p>
              <p className="text-lg font-semibold tabular-nums text-neutral-100">{s.v}</p>
              {s.note && <p className="text-xs text-neutral-500">{s.note}</p>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

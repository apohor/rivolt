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
// effMiPerKwh is the distance shown over the energy shown, or null while it would be absurd (a short drive, a state of charge that has not dropped).
export type SlotDrive = { tripMi: number; elapsed_sec: number; energy_used_kwh: number | null; effMiPerKwh: number | null };
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
  // A saved place the truck is at ("Home"), shown on the map; none otherwise.
  placeLabel?: string | null;
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
    if (d.effMiPerKwh != null && d.effMiPerKwh > 0) out.push({ k: "Efficiency", v: unit === "km" ? `${(d.effMiPerKwh * KM_PER_MI).toFixed(1)} km/kWh` : `${d.effMiPerKwh.toFixed(1)} mi/kWh` });
    if (d.energy_used_kwh != null && d.energy_used_kwh > 0) out.push({ k: "Energy used", v: `${d.energy_used_kwh.toFixed(1)} kWh` });
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

export type WeatherView = { temp: string; condition: string | null; icon: string | null; wind: string | null; precip: string | null };

// The weather of the parked slot: only what the server sent. By day a clear sky is "Sunny"; the 3 h figure is rain or snow by the current
// condition. Snow has no amount (the forecast is water-equivalent, which understates it); small rain amounts read "< 0.1 in" / "< 0.1 mm".
export function weatherView(w: SlotWeather, tempUnit: TemperatureUnit, unit: DistanceUnit, day = true): WeatherView {
  const cond = w.condition ? CONDITIONS[w.condition] : undefined;
  const label = cond ? (w.condition === "clear" && day ? "Sunny" : cond) : null;
  let precip: string | null = null;
  if (w.rain_next3h_mm != null) {
    const mm = w.rain_next3h_mm;
    if (w.condition === "snow") precip = mm < 0.05 ? "No snow next 3 h" : "Snow next 3 h";
    else if (mm < 0.05) precip = "No rain next 3 h";
    else {
      const amount = unit === "km" ? (mm < 0.1 ? "< 0.1 mm" : `${+mm.toFixed(1)} mm`) : mm / 25.4 < 0.1 ? "< 0.1 in" : `${+(mm / 25.4).toFixed(1)} in`;
      precip = `Rain next 3 h · ${amount}`;
    }
  }
  return {
    temp: formatTemperature(w.temp_c, tempUnit),
    condition: label ? `${label} here now` : null,
    icon: w.condition === "clear" && !day ? "night" : (w.condition ?? null),
    wind: w.wind_kph != null ? `Wind ${unit === "km" ? `${Math.round(w.wind_kph)} km/h` : `${Math.round(w.wind_kph / KM_PER_MI)} mph`}` : null,
    precip,
  };
}

// A small line drawing for the weather, 28 px, in the text colour.
function WeatherIcon({ kind }: { kind: string | null }) {
  if (!kind) return null;
  const sun = (cx: number, cy: number, r: number) => (
    <>
      <circle cx={cx} cy={cy} r={r} />
      {[0, 45, 90, 135, 180, 225, 270, 315].map((a) => (
        <line key={a} x1={cx} y1={cy - r - 2} x2={cx} y2={cy - r - 4} transform={`rotate(${a} ${cx} ${cy})`} />
      ))}
    </>
  );
  const cloud = <path d="M7 18h10a4 4 0 0 0 .5-7.97A5.5 5.5 0 0 0 7 11.5 3.25 3.25 0 0 0 7 18z" />;
  const drops = (n: number) => [0, 1, 2].slice(0, n).map((i) => <line key={i} x1={9 + i * 3.5} y1={20} x2={8 + i * 3.5} y2={22.5} />);
  let body: React.ReactNode = cloud;
  if (kind === "clear") body = sun(12, 12, 4);
  else if (kind === "night") body = <path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" />;
  else if (kind === "partly_cloudy") body = (<>{sun(8, 8, 2.8)}<path d="M9 20h8.5a3.5 3.5 0 0 0 .4-6.97A4.8 4.8 0 0 0 9 14a3 3 0 0 0 0 6z" /></>);
  else if (kind === "fog") body = (<><path d="M7 14h10a4 4 0 0 0 .5-7.97A5.5 5.5 0 0 0 7 7.5 3.25 3.25 0 0 0 7 14z" /><line x1="5" y1="18" x2="19" y2="18" /><line x1="8" y1="21" x2="16" y2="21" /></>);
  else if (kind === "drizzle") body = (<>{cloud}{drops(2)}</>);
  else if (kind === "rain") body = (<>{cloud}{drops(3)}</>);
  else if (kind === "snow") body = (<>{cloud}<circle cx="9" cy="21" r=".6" /><circle cx="12.5" cy="21" r=".6" /><circle cx="16" cy="21" r=".6" /></>);
  else if (kind === "thunder") body = (<>{cloud}<path d="M12.5 15l-2 3h3l-1.5 3" /></>);
  return (
    <svg data-testid="now-slot-icon" data-icon={kind} width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className="shrink-0 text-amber-500">
      {body}
    </svg>
  );
}

function Chip({ children, tone, testid }: { children: React.ReactNode; tone: "green" | "grey"; testid: string }) {
  return (
    <span data-testid={testid} className={`ml-2 inline-block rounded-md px-1.5 py-0.5 align-middle text-xs font-medium ${tone === "green" ? "bg-emerald-500/15 text-emerald-300" : "bg-neutral-800 text-neutral-300"}`}>
      {children}
    </span>
  );
}

const ROOT = "pointer-events-none relative mt-4 w-full min-w-0 border-t border-neutral-800 pt-4 min-[900px]:col-start-2 min-[900px]:row-span-2 min-[900px]:row-start-1 min-[900px]:mt-0 min-[900px]:self-start min-[900px]:border-t-0 min-[900px]:pt-0";

// Parked or plugged in: where the truck is on the map, then the weather there as one row (temperature and sky left, wind and rain right).
function ParkedSlot({ fix, weather, tempUnit, unit, kind, nowMs, placeLabel }: { fix: SlotFix; weather: SlotWeather | null; tempUnit: TemperatureUnit; unit: DistanceUnit; kind: string; nowMs: number; placeLabel: string | null }) {
  const hour = new Date(nowMs).getHours();
  const w = weather ? weatherView(weather, tempUnit, unit, hour >= 6 && hour < 20) : null;
  return (
    <div data-testid="now-slot" data-kind={kind} className={ROOT}>
      <ParkedMap lat={fix.lat} lon={fix.lon} label={placeLabel} caption={inDemo() ? "Sample map, not a real location" : null} />
      {w && (
        <div data-testid="now-slot-weather" className="mt-3 flex items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-2">
            <WeatherIcon kind={w.icon} />
            <div className="min-w-0">
              <p className="text-2xl font-semibold leading-none tabular-nums text-neutral-100" data-testid="now-slot-temp">{w.temp}</p>
              {w.condition && <p className="mt-1 text-sm text-neutral-400">{w.condition}</p>}
            </div>
          </div>
          <div className="shrink-0 text-right text-sm text-neutral-400">
            {w.wind && <p>{w.wind}</p>}
            {w.precip && <p>{w.precip}</p>}
          </div>
        </div>
      )}
    </div>
  );
}

// Null when the state has no slot, so the picture stays.
export default function NowSlot(i: SlotInput) {
  const { view, nowMs, unit } = i;
  if (!slotAvailable(view, i.fix ?? null)) return null;
  if (view.kind === "parked" || view.kind === "plugged") return <ParkedSlot fix={i.fix as SlotFix} weather={i.weather ?? null} tempUnit={i.tempUnit ?? "c"} unit={unit} kind={view.kind} nowMs={nowMs} placeLabel={i.placeLabel ?? null} />;
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

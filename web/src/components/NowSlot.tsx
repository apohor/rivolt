import { chargeFinishMs, clock, type NowView } from "../lib/nowState";
import { formatMoney } from "../lib/ledger";
import { formatDriveMode } from "../lib/driveMode";
import { formatTemperature, type DistanceUnit, type TemperatureUnit } from "../lib/preferences";
import { ParkedMap } from "../map/ParkedMap";
import { inDemo } from "../lib/demo";

// The state slot of the Now hero (design_preview "now_slot"): under the gauge, in place of the vehicle picture and the rows,
// the figures of what the truck is doing. Charging: power and the charger, when it reaches the limit, then the session. Driving:
// speed, gear and mode, then the drive so far. Only values that exist are shown; nothing is a placeholder.

export type SlotSession = { range_added_km: number; total_charged_energy_kwh: number; time_elapsed_seconds: number; kilometers_charged_per_hour: number };
export type SlotCharge = { started_at: string; soc_start?: number | null; cost?: number | null; currency?: string; cost_source?: string; cost_is_estimate?: boolean };
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
  // Design "now_parked_graph": the graph is the card's content, so the map gives way to one line saying where the truck is (null: no line, no row).
  placeText?: string | null;
  noMap?: boolean;
  // While asleep: when this sleep began and the seconds asleep today, shown under the map.
  asleep?: SlotAsleep | null;
  // Design "now_tidy": see TIDY_ROOT.
  tidy?: boolean;
  // Design "now_charge_graph": the figures as rows (the graph sits beside them), the target said once, and no plug-in time (the graph's start says it).
  soft?: boolean;
  // Design "now_drive_graph": the figures sit in the left half beside the graph, two to a row at every width.
  half?: boolean;
};
export type SlotAsleep = { since: string | null; todayS: number | null };
export type SlotFix = { lat: number; lon: number };
export type SlotWeather = { temp_c: number; condition?: string; wind_kph?: number };

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
// `up`: a time left rounds up to the whole minute (the iPhone's rule: never say less than there is); a duration so far rounds to the nearest.
function timeLeft(hours: number, up = false): string {
  const m = Math.max(1, up ? Math.ceil(hours * 60 - 1e-6) : Math.round(hours * 60));
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
    const c = i.charge;
    const startedMs = c ? Date.parse(c.started_at) : NaN;
    // The truck's own elapsed time; the recorded session's start otherwise.
    const elapsedSec = s && s.time_elapsed_seconds > 0 ? s.time_elapsed_seconds : Number.isFinite(startedMs) && i.nowMs > startedMs ? (i.nowMs - startedMs) / 1000 : null;
    // The truck's own figures come from the live session; without one (the demo, a home charge) the battery's gain at today's km per percent,
    // marked ≈ (as the iPhone's), and the rate from it once the charge is 5 min old.
    const truckRangeKm = s && s.range_added_km > 0 ? s.range_added_km : null;
    const addedPct = c?.soc_start != null && v.soc > c.soc_start ? v.soc - c.soc_start : null;
    const estRangeKm = truckRangeKm == null && addedPct != null && v.soc > 0 && v.rangeMi != null ? ((v.rangeMi * KM_PER_MI) / v.soc) * addedPct : null;
    const rangeKm = truckRangeKm ?? estRangeKm;
    if (rangeKm != null) out.push({ k: "Range added", v: `${truckRangeKm == null ? "≈ " : ""}+${dist(rangeKm / KM_PER_MI)}` });
    const truckRate = s && s.kilometers_charged_per_hour > 0 ? s.kilometers_charged_per_hour : null;
    const rate = truckRate ?? (rangeKm != null && elapsedSec != null && elapsedSec >= 300 ? rangeKm / (elapsedSec / 3600) : null);
    if (rate != null) out.push({ k: "Rate", v: `${truckRate == null ? "≈ " : ""}${dist(rate / KM_PER_MI)}/h` });
    if (elapsedSec != null) out.push({ k: "Elapsed", v: duration(elapsedSec) });
    if (c && c.cost != null) {
      const est = c.cost_is_estimate !== false;
      out.push({ k: "Cost so far", v: `${est ? "≈ " : ""}${formatMoney(c.cost, c.currency || i.currency)}`, note: (c.cost_source && COST_NOTE[c.cost_source]) || (est ? "estimate" : undefined) });
    }
    if (Number.isFinite(startedMs) && !i.soft) out.push({ k: "Plugged in", v: clock(startedMs) });
  } else if (v.kind === "driving" && i.drive) {
    const d = i.drive;
    if (d.tripMi > 0) out.push({ k: "Distance", v: dist(d.tripMi) });
    if (d.elapsed_sec > 0) out.push({ k: "Time", v: duration(d.elapsed_sec) });
    if (d.effMiPerKwh != null && d.effMiPerKwh > 0) out.push({ k: "Efficiency", v: unit === "km" ? `${(d.effMiPerKwh * KM_PER_MI).toFixed(1)} km/kWh` : `${d.effMiPerKwh.toFixed(1)} mi/kWh` });
    if (d.energy_used_kwh != null && d.energy_used_kwh > 0) out.push({ k: "Energy used", v: `${d.energy_used_kwh.toFixed(1)} kWh` });
  }
  return out;
}

// A sleeping truck is parked too: the map at its last fix is the most useful thing to show then.
const isParkedKind = (k: string) => k === "parked" || k === "plugged" || k === "asleep";

// The map's fix: the server's own point, else the live state's, else the server's last recorded position. A zero or non-finite pair is no fix.
export function pickFix(location: SlotFix | null | undefined, lat: number, lon: number, last: SlotFix | null | undefined): SlotFix | null {
  const ok = (p: { lat: number; lon: number } | null | undefined): p is SlotFix => p != null && Number.isFinite(p.lat) && Number.isFinite(p.lon) && (p.lat !== 0 || p.lon !== 0);
  if (ok(location)) return { lat: location.lat, lon: location.lon };
  if (ok({ lat, lon })) return { lat, lon };
  return ok(last) ? { lat: last.lat, lon: last.lon } : null;
}

// "Asleep for 2 h 15 min" and "Asleep today 18 h 40 min", each only when the server sent it.
export function asleepFigures(a: SlotAsleep | null | undefined, nowMs: number): { k: string; v: string }[] {
  const out: { k: string; v: string }[] = [];
  const since = a?.since ? Date.parse(a.since) : NaN;
  if (Number.isFinite(since) && nowMs >= since) out.push({ k: "Asleep for", v: timeLeft((nowMs - since) / 3_600_000) });
  if (a?.todayS != null && a.todayS >= 0) out.push({ k: "Asleep today", v: timeLeft(a.todayS / 3600) });
  return out;
}

// A truck whose newest frame says asleep keeps its slot past the stale (30 min) and offline (45 min) limits: sleeping for hours is normal under
// the sleep gate, and its position does not change. Past the offline limit the view's kind reads "offline", so the slot reads it as asleep.
export function slotView(v: NowView): NowView {
  return v.kind === "offline" && v.sleeping ? { ...v, kind: "asleep" } : v;
}

// Whether the Now card shows the slot: an asleep truck always (when it has a fix); every other state only while its frame is live.
export function slotShown(v: NowView, fix: SlotFix | null): boolean {
  const sv = slotView(v);
  const sleepy = sv.kind === "asleep" || (sv.kind === "plugged" && sv.sleeping);
  if (!sleepy && (v.kind === "offline" || v.stale)) return false;
  return slotAvailable(sv, fix);
}

// Whether the slot has its big figure (or, parked, a map to draw); when not, the picture stays.
export function slotAvailable(v: NowView, fix: SlotFix | null = null): boolean {
  return (v.kind === "charging" && v.powerKw != null) || (v.kind === "driving" && v.speedMph != null) || (isParkedKind(v.kind) && fix != null);
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

export type WeatherView = { temp: string; condition: string | null; icon: string | null; wind: string | null };

// The weather of the parked slot: only what the server sent. By day a clear sky is "Sunny"; no rain forecast.
export function weatherView(w: SlotWeather, tempUnit: TemperatureUnit, unit: DistanceUnit, day = true): WeatherView {
  const cond = w.condition ? CONDITIONS[w.condition] : undefined;
  const label = cond ? (w.condition === "clear" && day ? "Sunny" : cond) : null;
  return {
    temp: formatTemperature(w.temp_c, tempUnit),
    condition: label ? `${label} here now` : null,
    icon: w.condition === "clear" && !day ? "night" : (w.condition ?? null),
    wind: w.wind_kph != null ? `Wind ${unit === "km" ? `${Math.round(w.wind_kph)} km/h` : `${Math.round(w.wind_kph / KM_PER_MI)} mph`}` : null,
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

// Design "now_tidy": the slot is the map or the state's figures only; the outside conditions and the asleep-today figure move to one row of
// compact items under it (conditionItems), so the card reads in one hierarchy: status, battery, what the truck is doing, conditions.
const TIDY_ROOT = "pointer-events-none relative w-full min-w-0";

export type CondItem = { k: string; v: string; note?: string; info?: boolean };

// The conditions row: outside (the sky after the temperature), wind, the cabin, and while asleep the time asleep today.
// Same figures and wording as the weather block and rows they replace; "Asleep for" is the status line's "since" and is not repeated.
export function conditionItems(i: { weather: SlotWeather | null; tempUnit: TemperatureUnit; unit: DistanceUnit; nowMs: number; cabin: string | null; asleepTodayS: number | null; detail: boolean }): CondItem[] {
  const out: CondItem[] = [];
  const hour = new Date(i.nowMs).getHours();
  const w = i.weather ? weatherView(i.weather, i.tempUnit, i.unit, hour >= 6 && hour < 20) : null;
  if (w) {
    const sky = w.condition ? w.condition.replace(/ here now$/, "") : null;
    out.push({ k: "Outside", v: w.temp, ...(sky ? { note: sky } : {}), info: true });
    if (i.detail && w.wind) out.push({ k: "Wind", v: w.wind.replace(/^Wind /, "") });
  }
  if (i.cabin) out.push({ k: "Cabin", v: i.cabin });
  if (i.asleepTodayS != null && i.asleepTodayS >= 0) out.push({ k: "Asleep today", v: timeLeft(i.asleepTodayS / 3600) });
  return out;
}

// Parked or plugged in: where the truck is on the map, then the weather there as one row (temperature and sky left, wind right).
function ParkedSlot({ fix, weather, tempUnit, unit, kind, nowMs, placeLabel, placeText, noMap, asleep, tidy }: { tidy: boolean; placeText: string | null; noMap: boolean; fix: SlotFix; asleep: SlotAsleep | null; weather: SlotWeather | null; tempUnit: TemperatureUnit; unit: DistanceUnit; kind: string; nowMs: number; placeLabel: string | null }) {
  const hour = new Date(nowMs).getHours();
  const w = weather ? weatherView(weather, tempUnit, unit, hour >= 6 && hour < 20) : null;
  const sleepFigures = kind === "asleep" && !tidy ? asleepFigures(asleep, nowMs) : [];
  if (tidy && noMap)
    return placeText ? (
      <div data-testid="now-slot" data-kind={kind} className={TIDY_ROOT}>
        <p data-testid="now-parked-place" className="flex items-center gap-2 rounded-xl bg-neutral-800/50 px-3 py-2.5 text-sm text-neutral-200">
          <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="shrink-0 text-emerald-400">
            <path d="M12 21s7-6.2 7-11.5A7 7 0 0 0 5 9.5C5 14.8 12 21 12 21z" />
            <circle cx="12" cy="9.5" r="2.5" />
          </svg>
          <span className="min-w-0 truncate">{placeText}</span>
        </p>
      </div>
    ) : null;
  if (tidy)
    return (
      <div data-testid="now-slot" data-kind={kind} className={TIDY_ROOT}>
        <ParkedMap lat={fix.lat} lon={fix.lon} label={placeLabel} caption={inDemo() ? "Sample map, not a real location" : null} />
      </div>
    );
  return (
    <div data-testid="now-slot" data-kind={kind} className={ROOT}>
      <ParkedMap lat={fix.lat} lon={fix.lon} label={placeLabel} caption={inDemo() ? "Sample map, not a real location" : null} />
      {sleepFigures.length > 0 && (
        <div data-testid="now-slot-asleep" className="mt-3 grid grid-cols-2 gap-x-4">
          {sleepFigures.map((f) => (
            <div key={f.k} data-testid={`now-slot-${f.k.toLowerCase().replace(/ /g, "-")}`}>
              <p className="ui-label">{f.k}</p>
              <p className="text-2xl font-semibold leading-none tabular-nums text-neutral-100">{f.v}</p>
            </div>
          ))}
        </div>
      )}
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
  if (isParkedKind(view.kind)) return <ParkedSlot fix={i.fix as SlotFix} weather={i.weather ?? null} tempUnit={i.tempUnit ?? "c"} unit={unit} kind={view.kind} nowMs={nowMs} placeLabel={i.placeLabel ?? null} placeText={i.placeText ?? null} noMap={i.noMap ?? false} asleep={i.asleep ?? null} tidy={i.tidy ?? false} />;
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
      const when = clock(chargeFinishMs(view.etaHours, nowMs));
      sub = i.soft ? null : view.limitPct != null && view.limitPct < 100 ? `Reaches ${Math.round(view.limitPct)}% at ${when}` : `Full at ${when}`;
      small = `${timeLeft(view.etaHours, true)} left${view.etaSource === "truck" ? "" : " (est.)"}${view.limitPct != null && !i.soft ? ` · target ${Math.round(view.limitPct)}%` : ""}`;
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
    <div data-testid="now-slot" data-kind={view.kind} className={i.tidy ? TIDY_ROOT : ROOT}>
      <p className="text-4xl font-semibold leading-none tabular-nums text-neutral-100" data-testid="now-slot-big">
        {big}
        <span className="ml-1 text-base font-normal text-neutral-400">{bigUnit}</span>
        {chips}
      </p>
      {sub && <p className="mt-2 text-sm font-medium text-neutral-200" data-testid="now-slot-sub">{sub}</p>}
      {small && <p className="text-xs text-neutral-400" data-testid="now-slot-small">{small}</p>}
      {stats.length > 0 && !i.soft && (
        <div className={`mt-3 grid grid-cols-[repeat(2,minmax(min-content,1fr))] gap-x-2 gap-y-3 min-[360px]:gap-x-4 ${i.half ? "" : "min-[900px]:grid-cols-3"}`}>
          {stats.map((s) => (
            <div key={s.k} data-testid={`now-slot-${s.k.toLowerCase().replace(/ /g, "-")}`} className="min-w-0">
              <p className="ui-label">{s.k}</p>
              <p className="text-lg font-semibold tabular-nums text-neutral-100 max-[359px]:text-base"><StatValue v={s.v} /></p>
              {s.note && <p className="text-xs text-neutral-500">{s.note}</p>}
            </div>
          ))}
        </div>
      )}
      {stats.length > 0 && i.soft && (
        <dl className="mt-3 divide-y divide-neutral-800/60">
          {stats.map((s) => (
            <div key={s.k} data-testid={`now-slot-${s.k.toLowerCase().replace(/ /g, "-")}`} className="flex items-baseline justify-between gap-3 py-2">
              <dt className="text-sm text-neutral-400">{s.k}</dt>
              <dd className="text-right text-sm font-semibold tabular-nums text-neutral-100">
                {s.v}
                {s.note && <span className="ml-1.5 text-xs font-normal text-neutral-500">{s.note}</span>}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}

// "3.9 mi/kWh": the unit stays whole; on a narrow screen it is a smaller suffix and goes under the number rather than into the next cell.
function StatValue({ v }: { v: string }) {
  const m = v.match(/^(.+) ([^\s\d]+)$/);
  if (!m) return <>{v}</>;
  return (
    <>
      {m[1]} <span className="inline-block whitespace-nowrap max-[359px]:text-[0.6875rem] max-[359px]:font-normal max-[359px]:text-neutral-400">{m[2]}</span>
    </>
  );
}

import { chargeFinishMs, chargerLabel, clock, type ChargeFlavor, type NowView } from "../lib/nowState";
import { formatMoney } from "../lib/ledger";
import { formatDriveMode } from "../lib/driveMode";
import { formatTemperature, type DistanceUnit, type TemperatureUnit } from "../lib/preferences";
import type { LiveSession } from "../lib/api";
import { ParkedMap } from "../map/ParkedMap";
import { inDemo } from "../lib/demo";
import { useDesignPreview } from "../lib/config";
import { chargePulseState, PulseBolt } from "./ChargePulse";

// The state slot of the Now hero (design_preview "now_slot"): under the gauge, in place of the vehicle picture and the rows,
// the figures of what the truck is doing. Charging: power and the charger, when it reaches the limit, then the session. Driving:
// speed, gear and mode, then the drive so far. Only values that exist are shown; nothing is a placeholder.

export type SlotSession = { range_added_km: number; total_charged_energy_kwh: number; time_elapsed_seconds: number; kilometers_charged_per_hour: number; is_rivian_charger?: boolean };
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
  // A saved place the truck is at ("Home"), shown on the map; none otherwise.
  placeLabel?: string | null;
  // Design "now_parked_graph": the graph is the card's content, so the map gives way to one line saying where the truck is (null: no line, no row).
  placeText?: string | null;
  noMap?: boolean;
  // Design "now_charge_graph": the figures as rows (the graph sits beside them), the target said once, and no plug-in time (the graph's start says it).
  soft?: boolean;
  // Design "now_drive_graph": the figures sit in the left half beside the graph, two to a row at every width.
  half?: boolean;
};
export type SlotFix = { lat: number; lon: number };
export type SlotWeather = { temp_c: number; condition?: string; wind_kph?: number };

const KM_PER_MI = 1.609344;
// `est`: a figure we worked out ourselves (not the truck's own). The keyed card says so in words once instead of marking each with a sign; the unkeyed card marks each derived cell "est." and has no heading.
export type Stat = { k: string; v: string; note?: string; est?: boolean };

const COST_NOTE: Record<string, string> = {
  rivian: "billed by Rivian",
  user: "your price",
  home_rate: "at your home rate",
  network_estimate: "network rate",
  observed_estimate: "from similar sessions",
};

// An estimated cost always says which rate it used; when the source is missing or one we do not know, it says that instead of nothing.
export function costNote(source: string | undefined, est: boolean): string {
  return (source && COST_NOTE[source]) || (est ? "rate not known" : "");
}

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
    // The truck's own session figure when it reports one; the SoC-based estimate otherwise.
    if (s && s.total_charged_energy_kwh > 0) out.push({ k: "Added", v: `+${s.total_charged_energy_kwh.toFixed(1)} kWh` });
    else if (v.energyKwh != null && v.energyKwh > 0) out.push({ k: "Added", v: `+${v.energyKwh.toFixed(1)} kWh`, est: true });
    const c = i.charge;
    const startedMs = c ? Date.parse(c.started_at) : NaN;
    // The truck's own elapsed time; the recorded session's start otherwise.
    const elapsedSec = s && s.time_elapsed_seconds > 0 ? s.time_elapsed_seconds : Number.isFinite(startedMs) && i.nowMs > startedMs ? (i.nowMs - startedMs) / 1000 : null;
    // The truck's own figures come from the live session; without one (the demo, a home charge) the battery's gain at today's km per percent,
    // marked est. (as the iPhone's), and the rate from it once the charge is 5 min old.
    const truckRangeKm = s && s.range_added_km > 0 ? s.range_added_km : null;
    const addedPct = c?.soc_start != null && v.soc > c.soc_start ? v.soc - c.soc_start : null;
    const estRangeKm = truckRangeKm == null && addedPct != null && v.soc > 0 && v.rangeMi != null ? ((v.rangeMi * KM_PER_MI) / v.soc) * addedPct : null;
    const rangeKm = truckRangeKm ?? estRangeKm;
    if (rangeKm != null) out.push({ k: "Range added", v: `+${dist(rangeKm / KM_PER_MI)}`, est: truckRangeKm == null });
    const truckRate = s && s.kilometers_charged_per_hour > 0 ? s.kilometers_charged_per_hour : null;
    const rate = truckRate ?? (rangeKm != null && elapsedSec != null && elapsedSec >= 300 ? rangeKm / (elapsedSec / 3600) : null);
    if (rate != null) out.push({ k: "Rate", v: `${dist(rate / KM_PER_MI)}/h`, est: truckRate == null });
    if (elapsedSec != null) out.push({ k: "Elapsed", v: duration(elapsedSec) });
    if (c && c.cost != null) {
      const est = c.cost_is_estimate !== false;
      out.push({ k: "Cost so far", v: formatMoney(c.cost, c.currency || i.currency), note: [est ? "estimate" : "", costNote(c.cost_source, est)].filter(Boolean).join(" · ") || undefined, est });
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

// Rivian's own price for the open session, when its latest read has one above zero (a zero is the feed before it has priced the session; a free
// session has no price; an ended session is the previous plug-in's, not this charge's): newer than the charge row's snapshot of it, so the Now card and the Live page, reading the same session, say the same cost.
export function freshCharge(charge: SlotCharge | null, ls: LiveSession | null): SlotCharge | null {
  const price = ls?.active && !ls.is_free_session ? Number(ls.current_price) : 0;
  if (!(price > 0)) return charge;
  return { started_at: "", ...charge, cost: price, currency: ls?.current_currency || charge?.currency, cost_source: "rivian", cost_is_estimate: false };
}

// The charge as the cards show it: Rivian's latest price over the row's snapshot (freshCharge), and no home-rate cost unless the home rate is a fair price
// for it. A home charge keeps its estimate; a fast charge or AC away never has one; while the place or peak is not known the estimate waits for the
// live session to say the charger is not a Rivian one (until it has answered, and if its read fails, there is no cost rather than a wrong one).
export function shownCharge(charge: SlotCharge | null, ls: LiveSession | null, flavor: ChargeFlavor | null): SlotCharge | null {
  const c = freshCharge(charge, ls);
  if (!c || c.cost == null || c.cost_source !== "home_rate") return c;
  const fits = flavor === "home_l2" || (flavor == null && !!ls && !ls.is_rivian_charger);
  if (fits) return c;
  const { cost: _cost, currency: _currency, cost_source: _source, cost_is_estimate: _est, ...rest } = c;
  return rest;
}

// Whether the charge's own active Rivian session says it is free, and a reader can believe it: a Rivian charger (the Adventure Network, a Waypoints
// charger) on a charge the home rate does not fit (a fast charge, or AC away). Rivian marks any charge it does not bill as "free": a home charge on
// a Wall Charger and another network's charger included, and none of those is a free charge. (The server's liveSessionIsFree is the same rule.)
export function sessionIsFree(ls: LiveSession | null | undefined, flavor: ChargeFlavor | null): boolean {
  return !!ls?.active && ls.is_free_session && ls.is_rivian_charger && (flavor === "dcfc" || flavor === "away_ac");
}

// The Live page's figures for a charge: the Now card's own (slotStats: the truck's session when it reports one, else worked out from the battery),
// plus the time left (the truck's, else the card's estimate). No "Plugged in" (the Live page has the graph's start for it).
export function liveStats(i: SlotInput, remainingSec: number): Stat[] {
  const out = slotStats({ ...i, soft: true });
  if (remainingSec > 0) out.push({ k: "Remaining", v: timeLeft(remainingSec / 3600, true) });
  else if (i.view.etaHours != null) out.push({ k: "Remaining", v: timeLeft(i.view.etaHours, true), est: i.view.etaSource !== "truck" });
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

function Chip({ children, tone, testid }: { children: React.ReactNode; tone: "green" | "grey"; testid: string }) {
  return (
    <span data-testid={testid} className={`ml-2 inline-block rounded-md px-1.5 py-0.5 align-middle text-xs font-medium ${tone === "green" ? "bg-emerald-500/15 text-emerald-300" : "bg-neutral-800 text-neutral-300"}`}>
      {children}
    </span>
  );
}

// The slot is the map or the state's figures only; the outside conditions and the asleep-today figure move to one row of
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

// Parked or plugged in: where the truck is on the map (or one line saying where, when a graph is the card's content).
function ParkedSlot({ fix, kind, placeLabel, placeText, noMap }: { placeText: string | null; noMap: boolean; fix: SlotFix; kind: string; placeLabel: string | null }) {
  if (noMap)
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
  return (
    <div data-testid="now-slot" data-kind={kind} className={TIDY_ROOT}>
      <ParkedMap lat={fix.lat} lon={fix.lon} label={placeLabel} caption={inDemo() ? "Sample map, not a real location" : null} />
    </div>
  );
}

// Null when the state has no slot, so the picture stays.
export default function NowSlot(i: SlotInput) {
  const { view, nowMs, unit } = i;
  // Design "charge_pulse": the bolt beside the kW figure while power flows.
  const pulse = chargePulseState(useDesignPreview("charge_pulse"), view.powerKw, false);
  if (!slotAvailable(view, i.fix ?? null)) return null;
  if (isParkedKind(view.kind)) return <ParkedSlot fix={i.fix as SlotFix} kind={view.kind} placeLabel={i.placeLabel ?? null} placeText={i.placeText ?? null} noMap={i.noMap ?? false} />;
  const stats = slotStats(i);
  let big = "";
  let bigUnit = "";
  let sub: string | null = null;
  let small: string | null = null;
  const chips: React.ReactNode[] = [];
  if (view.kind === "charging" && view.powerKw != null) {
    big = view.powerKw.toFixed(view.powerKw < 10 ? 1 : 0);
    bigUnit = "kW";
    // One rule with the Live page: the truck's own Rivian-charger flag wins, else the flavour.
    const label = chargerLabel(view.chargeFlavor, !!i.session?.is_rivian_charger);
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
    <div data-testid="now-slot" data-kind={view.kind} className={TIDY_ROOT}>
      <p className="text-4xl font-semibold leading-none tabular-nums text-neutral-100" data-testid="now-slot-big">
        {big}
        <span className="ml-1 text-base font-normal text-neutral-400">{bigUnit}</span>
        {view.kind === "charging" && <PulseBolt state={pulse} />}
        {chips}
      </p>
      {sub && <p className="mt-2 text-sm font-medium text-neutral-200" data-testid="now-slot-sub">{sub}</p>}
      {small && <p className="text-xs text-neutral-400" data-testid="now-slot-small">{small}</p>}
      {stats.length > 0 && !i.soft && (
        <div className={`mt-3 grid grid-cols-[repeat(2,minmax(min-content,1fr))] gap-x-2 gap-y-3 min-[360px]:gap-x-4 ${i.half ? "" : "min-[900px]:grid-cols-3"}`}>
          {allEstimated(stats) && <p data-testid="now-slot-estimated" className="col-span-full ui-label">Estimated so far</p>}
          {stats.map((s) => (
            <div key={s.k} data-testid={`now-slot-${s.k.toLowerCase().replace(/ /g, "-")}`} className="min-w-0">
              <p className="ui-label">{s.k}</p>
              <p className="text-lg font-semibold tabular-nums text-neutral-100 max-[359px]:text-base">
                {s.est && <span className="sr-only">estimated </span>}
                <StatValue v={s.v} />
                {s.est && !allEstimated(stats) && s.k !== "Cost so far" && <span aria-hidden="true" className="ml-1 text-xs font-normal text-neutral-500">est.</span>}
              </p>
              {s.note && <p className="text-xs text-neutral-500">{s.note}</p>}
            </div>
          ))}
        </div>
      )}
      {stats.length > 0 && i.soft && <SoftStats stats={stats} />}
    </div>
  );
}

// Elapsed and Plugged in are clock facts, out of the count: one heading says so when every other figure is our own estimate (both grids use this rule).
export function allEstimated(stats: Stat[]) {
  const counted = stats.filter((s) => s.k !== "Elapsed" && s.k !== "Plugged in");
  return counted.length > 0 && counted.every((s) => s.est);
}

// The keyed card's figures: a tight two-column grid of label-over-value cells. When every worked-out figure (all but the elapsed time) is our own
// estimate, one quiet heading says so inside the grid; when the set is mixed, only the derived cells carry a small "est." (the cost keeps the word
// "estimate" and its source note, heading or not). A truck-reported figure never carries a mark. Screen readers get "estimated" with each estimated value in both cases.
function SoftStats({ stats }: { stats: Stat[] }) {
  const all = allEstimated(stats);
  return (
    <div data-testid="now-slot-figures" className="mt-2 grid grid-cols-2 gap-x-4 gap-y-2">
      {all && <p data-testid="now-slot-estimated" className="col-span-2 ui-label">Estimated so far</p>}
      {stats.map((s) => {
        const note = s.note;
        return (
          <div key={s.k} data-testid={`now-slot-${s.k.toLowerCase().replace(/ /g, "-")}`} className="min-w-0">
            <p className="ui-label">{s.k}</p>
            <p className="text-base font-semibold tabular-nums text-neutral-100">
              {s.est && <span className="sr-only">estimated </span>}
              {s.v}
              {s.est && !all && s.k !== "Cost so far" && <span aria-hidden="true" className="ml-1 text-xs font-normal text-neutral-500">est.</span>}
              {note && <span className="ml-1.5 text-xs font-normal text-neutral-500">{note}</span>}
            </p>
          </div>
        );
      })}
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

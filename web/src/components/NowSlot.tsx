import { clock, formatEta, type NowView } from "../lib/nowState";
import { formatMoney } from "../lib/ledger";
import type { DistanceUnit } from "../lib/preferences";

// The state slot of the Now hero (design_preview "now_slot"): under the gauge, in place of the vehicle picture,
// the figures of what the truck is doing. Charging: power, when it reaches the limit, energy added and cost so far.
// Driving: speed and the drive so far. Only values that exist are shown; nothing is a placeholder.

export type SlotDrive = { distance_mi: number; elapsed_sec: number; energy_used_kwh: number; soc_used_pct: number };

const KM_PER_MI = 1.609344;

function Stat({ label, value, testid }: { label: string; value: string; testid: string }) {
  return (
    <div data-testid={testid}>
      <p className="ui-label">{label}</p>
      <p className="text-lg font-semibold tabular-nums text-neutral-100">{value}</p>
    </div>
  );
}

function duration(sec: number): string {
  const m = Math.max(0, Math.round(sec / 60));
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")}`;
}

export function slotStats(
  v: NowView,
  drive: SlotDrive | null,
  nowMs: number,
  unit: DistanceUnit,
  currency: string,
): { k: string; v: string }[] {
  const out: { k: string; v: string }[] = [];
  if (v.kind === "charging") {
    if (v.etaHours != null) out.push({ k: "Time left", v: formatEta(v.etaHours, nowMs, v.etaSource !== "truck") });
    if (v.energyKwh != null && v.energyKwh > 0) out.push({ k: "Added", v: `${v.energyKwh.toFixed(1)} kWh` });
    if (v.costSoFar) out.push({ k: "Cost so far", v: formatMoney(v.costSoFar.amount, currency) });
  } else if (v.kind === "driving" && drive) {
    const dist = unit === "km" ? drive.distance_mi * KM_PER_MI : drive.distance_mi;
    if (drive.distance_mi > 0) out.push({ k: "Distance", v: `${dist.toFixed(1)} ${unit}` });
    if (drive.elapsed_sec > 0) out.push({ k: "Time", v: duration(drive.elapsed_sec) });
    // Same guard as the Efficiency row: not before it can be absurd.
    if (drive.distance_mi >= 1 && drive.soc_used_pct >= 1 && drive.energy_used_kwh > 0) {
      const e = drive.distance_mi / drive.energy_used_kwh;
      out.push({ k: "Efficiency", v: unit === "km" ? `${(100 / (e * KM_PER_MI)).toFixed(1)} kWh/100 km` : `${e.toFixed(1)} mi/kWh` });
    }
    if (drive.energy_used_kwh > 0) out.push({ k: "Energy used", v: `${drive.energy_used_kwh.toFixed(1)} kWh` });
  }
  return out;
}

// Whether the slot has its big figure; when not, the picture stays.
export function slotAvailable(v: NowView): boolean {
  return (v.kind === "charging" && v.powerKw != null) || (v.kind === "driving" && v.speedMph != null);
}

// Null when the state has no slot (or nothing to show yet), so the picture stays.
export default function NowSlot({
  view,
  drive,
  nowMs,
  unit,
  currency,
}: {
  view: NowView;
  drive: SlotDrive | null;
  nowMs: number;
  unit: DistanceUnit;
  currency: string;
}) {
  const stats = slotStats(view, drive, nowMs, unit, currency);
  let big: { value: string; unit: string } | null = null;
  let sub: string | null = null;
  if (view.kind === "charging" && view.powerKw != null) {
    big = { value: view.powerKw.toFixed(view.powerKw < 10 ? 1 : 0), unit: "kW" };
    if (view.etaHours != null) {
      const when = clock(nowMs + view.etaHours * 3_600_000);
      sub = view.limitPct != null && view.limitPct < 100 ? `Reaches ${Math.round(view.limitPct)}% at ${when}` : `Full at ${when}`;
    }
  } else if (view.kind === "driving" && view.speedMph != null) {
    const s = unit === "km" ? view.speedMph * KM_PER_MI : view.speedMph;
    big = { value: String(Math.round(s)), unit: unit === "km" ? "km/h" : "mph" };
    sub = stats.length > 0 ? "This drive so far" : null;
  }
  if (!big) return null;
  return (
    <div data-testid="now-slot" data-kind={view.kind} className="w-full min-w-0 min-[900px]:col-start-2 min-[900px]:row-span-2 min-[900px]:row-start-1">
      <p className="text-4xl font-semibold leading-none tabular-nums text-neutral-100" data-testid="now-slot-big">
        {big.value}
        <span className="ml-1 text-base font-normal text-neutral-400">{big.unit}</span>
      </p>
      {sub && <p className="mt-1 text-sm text-neutral-400" data-testid="now-slot-sub">{sub}</p>}
      {stats.length > 0 && (
        <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 min-[900px]:grid-cols-4">
          {stats.map((s) => (
            <Stat key={s.k} label={s.k} value={s.v} testid={`now-slot-${s.k.toLowerCase().replace(/ /g, "-")}`} />
          ))}
        </div>
      )}
    </div>
  );
}

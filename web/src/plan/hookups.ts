// The hookups a night's stop can have, with the power planned for each (an estimate) and what to say about it. Rivian's own
// name for the cord is "Portable Charger". The default is 7.2 kW; the user can edit the power (1.0 to 11.5 kW).
export type HookupId = "j1772" | "nema_14_50" | "tt_30" | "outlet_120" | "none";

export type Hookup = { id: HookupId; label: string; kw: number; min: number; max: number; note: string };

// Bounds follow the plan request: the server rejects a power outside them with a 400, so the field keeps to the same range.
export const HOOKUPS: Hookup[] = [
  { id: "j1772", label: "J1772 charging station", kw: 7.0, min: 1.0, max: 11.5, note: "The station's power varies; planned at {kw} kW (estimate)." },
  {
    id: "nema_14_50",
    label: "NEMA 14-50 with the Rivian Portable Charger",
    kw: 7.2,
    min: 1.0,
    max: 7.6,
    note: "Up to 7.6 kW with Rivian's Portable Charger; planned at {kw} kW (estimate) for campground voltage. You need to carry the Portable Charger.",
  },
  { id: "tt_30", label: "30 A (TT-30): slow, needs an adapter", kw: 1.4, min: 1.0, max: 1.4, note: "30 A, 120 V: needs an adapter not included with the truck; slow (about {kw} kW, estimate)." },
  { id: "outlet_120", label: "120 V outlet: slow", kw: 1.4, min: 1.0, max: 1.4, note: "Slow, 120 V (about {kw} kW, estimate)." },
  { id: "none", label: "No charger", kw: 0, min: 0, max: 0, note: "With no charger, you leave with the charge you arrive with." },
];

export const DEFAULT_HOOKUP: HookupId = "j1772";
export const DEFAULT_OVERNIGHT_KW = 7.0;
// Existing hotel stops (no hookup chosen) keep the wider range.
export const KW_MIN = 1.0;
export const KW_MAX = 19.2;

export const hookupById = (id: string | null | undefined): Hookup | undefined => HOOKUPS.find((h) => h.id === id);

// The note with the planned power filled in.
export const hookupNote = (id: string | null | undefined, kw: number): string => (hookupById(id)?.note ?? "").replace("{kw}", String(kw));

// The kW the plan request gets: the edited power kept inside the hookup's range.
export function clampKw(kw: number, hookup?: HookupId): number {
  const h = hookupById(hookup);
  const lo = h ? h.min : KW_MIN;
  const hi = h ? h.max : KW_MAX;
  if (!Number.isFinite(kw)) return h ? h.kw : DEFAULT_OVERNIGHT_KW;
  return Math.min(hi, Math.max(lo, Math.round(kw * 10) / 10));
}

// A hookup known only by its power (a saved trip from before hookups, or a listed hotel charger): the nearest preset by kW.
export function hookupForKw(kw: number): HookupId {
  if (kw <= 0) return "none";
  if (kw <= 1.4) return "outlet_120";
  return "j1772";
}

// The overnight row's line: "Overnight at X, NEMA 14-50 ..., about 7.2 kW (estimate)".
export function stayLine(name: string, hookup: HookupId | undefined, kw: number): string {
  const h = hookupById(hookup);
  if (!h || h.id === "none") return `Overnight at ${name}`;
  return `Overnight at ${name}, ${h.label}, about ${kw} kW (estimate)`;
}

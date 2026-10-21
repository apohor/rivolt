// The hookups a night's stop can have, with the power planned for each (an estimate) and what to say about it. Rivian's own
// name for the cord is "Portable Charger". The default is 7.2 kW; the user can edit the power (1.0 to 11.5 kW).
export type HookupId = "j1772" | "nema_14_50" | "tt_30" | "outlet_120" | "none";

export type Hookup = { id: HookupId; label: string; kw: number; note: string };

export const HOOKUPS: Hookup[] = [
  { id: "j1772", label: "J1772 charging station", kw: 7.2, note: "The station's power varies; planned at 7.2 kW (estimate)." },
  {
    id: "nema_14_50",
    label: "NEMA 14-50 with the Rivian Portable Charger",
    kw: 7.2,
    note: "Up to 7.6 kW with Rivian's Portable Charger; planned at 7.2 kW (estimate) for campground voltage. You need to carry the Portable Charger.",
  },
  { id: "tt_30", label: "30 A (TT-30): slow, needs an adapter", kw: 2.9, note: "Slow: about 2.9 kW (estimate). Needs an adapter not included with the truck." },
  { id: "outlet_120", label: "120 V outlet: slow", kw: 1.4, note: "Slow: 120 V, about 1.4 kW (estimate)." },
  { id: "none", label: "No charger", kw: 0, note: "With no charger, you leave with the charge you arrive with." },
];

export const DEFAULT_HOOKUP: HookupId = "j1772";
export const DEFAULT_OVERNIGHT_KW = 7.2;
export const KW_MIN = 1.0;
export const KW_MAX = 11.5;

export const hookupById = (id: string | null | undefined): Hookup | undefined => HOOKUPS.find((h) => h.id === id);

// The kW the plan request gets: none is 0, otherwise the edited power kept in range.
export const clampKw = (kw: number): number => (Number.isFinite(kw) ? Math.min(KW_MAX, Math.max(KW_MIN, Math.round(kw * 10) / 10)) : DEFAULT_OVERNIGHT_KW);

// A hookup known only by its power (a saved trip from before hookups, or a listed hotel charger): the nearest preset by kW.
export function hookupForKw(kw: number): HookupId {
  if (kw <= 0) return "none";
  if (Math.abs(kw - 2.9) < 0.05) return "tt_30";
  if (Math.abs(kw - 1.4) < 0.05) return "outlet_120";
  return "j1772";
}

// The overnight row's line: "Overnight at X, NEMA 14-50 ..., about 7.2 kW (estimate)".
export function stayLine(name: string, hookup: HookupId | undefined, kw: number): string {
  const h = hookupById(hookup);
  if (!h || h.id === "none") return `Overnight at ${name}`;
  return `Overnight at ${name}, ${h.label}, about ${kw} kW (estimate)`;
}

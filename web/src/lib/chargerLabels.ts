import labels from "./chargerLabels.json";
import { AC_MAX_KW, DCFC_MIN_KW } from "./ledger";

// The charger-type words, one set for the whole app (and the iPhone app, which gets chargerLabels.json through ios/design/charger-labels.json):
// Fast (DC, 50 kW and up), Medium (Level 2), Hotel, Camping, Outlet. Every word and every one-sentence explanation lives in chargerLabels.json, so a
// change of wording is one edit there. "Level 2" and "DC" appear only in the explanations.
export type ChargerKind = keyof typeof labels.types;
export const CHARGER_KINDS = Object.keys(labels.types) as ChargerKind[];
export const ALL_LABEL: string = labels.all;
export const INFO_LABEL: string = labels.infoLabel;

export function chargerLabel(kind: ChargerKind): string {
  return labels.types[kind].label;
}
export function chargerSentence(kind: ChargerKind): string {
  return labels.types[kind].sentence;
}

// Which type a power is: a wall outlet at 2 kW or less; Fast from the ledger's DC threshold; Medium (Level 2) up to the most a vehicle takes on AC;
// the band between (Slow DC) is DC that stayed under the fast threshold. `dc` says what is known of the connector: true or false from a station's
// sockets (so a 22 kW AC station is Medium, not DC), unknown (a measured session peak) judged by power alone. Null when there is no power.
export function chargerKindOfKw(kw: number | null | undefined, dc?: boolean | null): ChargerKind | null {
  if (kw == null || !Number.isFinite(kw) || kw <= 0) return null;
  if (kw <= 2) return "outlet";
  if (dc === false) return "medium";
  if (kw >= DCFC_MIN_KW) return "fast";
  if (dc === true) return "slowdc";
  return kw <= AC_MAX_KW ? "medium" : "slowdc";
}

// A station's own rating next to its type: "Fast · station up to 250 kW". The word alone when there is no rating. (A session's measured peak is the
// vehicle's, not the station's; charge detail says "peak".)
export function stationLine(kind: ChargerKind, ratedKw?: number | null): string {
  return ratedKw != null && ratedKw > 0 ? `${chargerLabel(kind)} · station up to ${Math.round(ratedKw)} kW` : chargerLabel(kind);
}

// What a station's sockets say about AC or DC: true when one is a DC connector (a combo CCS, CHAdeMO, a Supercharger), false when all are AC
// (Type 1 / Type 2 / J1772), unknown (null) when none are listed or they are ambiguous (NACS can be either).
export function stationIsDc(sockets: string[] | undefined): boolean | null {
  if (!sockets || sockets.length === 0) return null;
  if (sockets.some((s) => /combo|chademo|supercharger|ccs/i.test(s))) return true;
  if (sockets.every((s) => /^(type1|type2|j1772|type2_cable|iec)/i.test(s))) return false;
  return null;
}

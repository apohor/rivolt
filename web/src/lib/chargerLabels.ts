import labels from "./chargerLabels.json";
import { DCFC_MIN_KW } from "./ledger";

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

// Which type a power rating is: a wall outlet at 2 kW or less, Fast from the ledger's DC threshold, Medium (Level 2) between. Null when there is no rating.
export function chargerKindOfKw(kw: number | null | undefined): ChargerKind | null {
  if (kw == null || !Number.isFinite(kw) || kw <= 0) return null;
  if (kw <= 2) return "outlet";
  return kw >= DCFC_MIN_KW ? "fast" : "medium";
}

// A station's own rating next to its type: "Fast · station up to 250 kW". The word alone when there is no rating. (A session's measured peak is the
// vehicle's, not the station's; charge detail says "peak".)
export function stationLine(kind: ChargerKind, ratedKw?: number | null): string {
  return ratedKw != null && ratedKw > 0 ? `${chargerLabel(kind)} · station up to ${Math.round(ratedKw)} kW` : chargerLabel(kind);
}

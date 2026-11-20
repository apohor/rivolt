// How tall the Now card was the last time it was drawn, per vehicle, layout and kind of card, so its placeholder (and the card itself, while its graph
// is on the way) can be that tall on the next load. A charging or driving card with its graph is taller than a parked one; with the last height the
// page below does not jump when the state and the series arrive. Only pixel counts under the vehicle's id, and which card was drawn last (no state,
// no place); sign-out clears it.
import { readStoredID } from "./selectedVehicle";

const KEY = "rivolt.nowCardHeights";
const LAST = "rivolt.nowCardLast";

export type CardKind = "parked" | "charging" | "driving";

// Plugged, asleep and offline cards are the parked kind: they share the layout.
export const cardKind = (viewKind: string): CardKind => (viewKind === "charging" || viewKind === "driving" ? viewKind : "parked");

// The hero is two columns from 900 px and the page puts it in a grid from 1024 px: three layouts, three heights.
function layout(): "narrow" | "mid" | "wide" {
  if (typeof window === "undefined" || !window.matchMedia) return "narrow";
  if (window.matchMedia("(min-width: 1024px)").matches) return "wide";
  return window.matchMedia("(min-width: 900px)").matches ? "mid" : "narrow";
}

const slot = (vehicleID: string, redesigned: boolean, kind: CardKind) => `${vehicleID}:${redesigned ? "v2" : "v1"}:${layout()}:${kind}`;

function read(): Record<string, number> {
  try {
    const o = JSON.parse(window.localStorage.getItem(KEY) || "{}");
    return o && typeof o === "object" ? o : {};
  } catch {
    return {};
  }
}

type Last = { id: string; v2: boolean; kind: CardKind };

function readLast(): Last | null {
  try {
    const o = JSON.parse(window.localStorage.getItem(LAST) || "null");
    return o && typeof o.id === "string" && typeof o.v2 === "boolean" && (o.kind === "parked" || o.kind === "charging" || o.kind === "driving") ? o : null;
  } catch {
    return null;
  }
}

const sane = (h: unknown): number | null => (typeof h === "number" && h >= 120 && h <= 1200 ? h : null);

// The height of this kind of card for this vehicle, or null when it has not been drawn in this layout yet. `hero` is the card's own box, without the
// alerts under it (what a hold keeps); otherwise the whole section (what the placeholder takes).
export function cardHeight(vehicleID: string, redesigned: boolean, kind: CardKind, hero = false): number | null {
  return sane(read()[slot(vehicleID, redesigned, kind) + (hero ? ":hero" : "")]);
}

// The placeholder's height before anything is known: the card as it was last drawn. The vehicle is the one the list names, or, before the list has
// answered, the one the user last picked; the design and the kind of card are the ones drawn last (the design flag is not known until /api/config
// has answered either).
export function placeholderHeight(vehicleID: string | undefined): number | null {
  const id = vehicleID ?? readStoredID();
  const last = readLast();
  if (!id || !last) return null;
  return cardHeight(id, last.v2, last.id === id ? last.kind : "parked");
}

export function rememberCardHeight(vehicleID: string, redesigned: boolean, kind: CardKind, height: number, heroHeight?: number) {
  const all = read();
  const s = slot(vehicleID, redesigned, kind);
  let changed = false;
  for (const [k, v] of [[s, height], [s + ":hero", heroHeight]] as const) {
    if (v == null) continue;
    const h = Math.round(v);
    if (h < 120 || h > 1200 || Math.abs((all[k] ?? 0) - h) < 4) continue;
    all[k] = h;
    changed = true;
  }
  try {
    const last = JSON.stringify({ id: vehicleID, v2: redesigned, kind });
    if (window.localStorage.getItem(LAST) !== last) window.localStorage.setItem(LAST, last);
    if (changed) window.localStorage.setItem(KEY, JSON.stringify(all));
  } catch {
    /* private mode: the placeholder falls back to the parked height */
  }
}

export function forgetCardHeights() {
  try {
    window.localStorage.removeItem(KEY);
    window.localStorage.removeItem(LAST);
  } catch {
    /* nothing stored */
  }
}

// How tall the Now card was the last time it was drawn, per vehicle and layout, so its placeholder can be that tall on the next load.
// A charging or driving card with its graph is taller than a parked one; with the last height the page below does not jump when the state arrives.
// Only a pixel count under the vehicle's id is kept (no state, no place); sign-out clears it.
const KEY = "rivolt.nowCardHeights";

const slot = (vehicleID: string, redesigned: boolean) =>
  `${vehicleID}:${redesigned ? "v2" : "v1"}:${typeof window !== "undefined" && window.matchMedia?.("(min-width: 1024px)").matches ? "wide" : "narrow"}`;

function read(): Record<string, number> {
  try {
    const o = JSON.parse(window.localStorage.getItem(KEY) || "{}");
    return o && typeof o === "object" ? o : {};
  } catch {
    return {};
  }
}

export function lastCardHeight(vehicleID: string | undefined, redesigned: boolean): number | null {
  if (!vehicleID) return null;
  const h = read()[slot(vehicleID, redesigned)];
  return typeof h === "number" && h >= 120 && h <= 1200 ? h : null;
}

export function rememberCardHeight(vehicleID: string, redesigned: boolean, height: number) {
  const h = Math.round(height);
  if (h < 120 || h > 1200) return;
  const all = read();
  const s = slot(vehicleID, redesigned);
  if (Math.abs((all[s] ?? 0) - h) < 4) return;
  all[s] = h;
  try {
    window.localStorage.setItem(KEY, JSON.stringify(all));
  } catch {
    /* private mode: the placeholder falls back to the parked height */
  }
}

export function forgetCardHeights() {
  try {
    window.localStorage.removeItem(KEY);
  } catch {
    /* nothing stored */
  }
}

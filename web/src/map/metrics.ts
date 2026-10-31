// The drive map's colour modes as pure functions: which modes a drive supports, the per-sample values of each, the 5th-to-95th percentile
// range they are scaled to, and the gradient stops for MapLibre's `line-gradient`. No map library, no DOM.

export type Mode = "speed" | "eff" | "elev";
export type MapFlavor = "dark" | "light";

// One sample of the drive as the map sees it: position, and what each mode needs (all optional).
export type Sample = {
  lat: number;
  lon: number;
  t?: number;
  s?: number; // speed, mph
  alt?: number; // elevation, metres
  soc?: number; // battery level, percent
  odo?: number; // odometer, miles
};

// Ramps per flavour (ux, from the prototype): speed is blue → teal → yellow, efficiency red → yellow → teal (low is bad), elevation four stops.
// The dark ones are the app tokens (drive, accent, fast / critical, estimate, accent); the light ones keep the hues at map-line strength.
export const RAMPS: Record<MapFlavor, Record<Mode, string[]>> = {
  dark: {
    speed: ["#74D4FF", "#5EE9B5", "#FFB900"],
    eff: ["#FF6B81", "#FFD230", "#5EE9B5"],
    elev: ["#4C8DFF", "#5EE9B5", "#FFD230", "#FE9A00"],
  },
  light: {
    speed: ["#2B7DE9", "#0FA37F", "#F0A000"],
    eff: ["#D6335A", "#F0A000", "#0FA37F"],
    elev: ["#2B66E0", "#0FA37F", "#F0A000", "#D9480F"],
  },
};

// Thresholds (ux): efficiency needs a real drive and a real battery change; elevation needs nearly every sample to carry an altitude.
export const EFF_MIN_MILES = 5;
export const EFF_MIN_SOC_CHANGE = 5;
export const ELEV_MIN_COVERAGE = 0.8;

const finite = (n: number | undefined): n is number => typeof n === "number" && Number.isFinite(n);

// percentileRange is the [lo, hi] percentile of the finite values (linear interpolation), so one spike does not wash out the rest.
export function percentileRange(vals: number[], loPct = 5, hiPct = 95): [number, number] {
  const v = vals.filter(Number.isFinite).sort((a, b) => a - b);
  if (v.length === 0) return [0, 1];
  const at = (p: number) => {
    const x = (p / 100) * (v.length - 1);
    const i = Math.floor(x);
    const j = Math.min(v.length - 1, i + 1);
    return v[i] + (v[j] - v[i]) * (x - i);
  };
  const lo = at(loPct);
  const hi = at(hiPct);
  return hi > lo ? [lo, hi] : [lo, lo + 1e-6];
}

// cumulativeMiles: the distance along the samples (haversine), in miles, 0 at the first.
export function cumulativeMiles(s: Sample[]): number[] {
  const out = [0];
  for (let i = 1; i < s.length; i++) {
    out.push(out[i - 1] + haversineMi(s[i - 1].lat, s[i - 1].lon, s[i].lat, s[i].lon));
  }
  return out;
}

export function haversineMi(la1: number, lo1: number, la2: number, lo2: number): number {
  const R = 3958.8;
  const rad = Math.PI / 180;
  const dLa = (la2 - la1) * rad;
  const dLo = (lo2 - lo1) * rad;
  const a = Math.sin(dLa / 2) ** 2 + Math.cos(la1 * rad) * Math.cos(la2 * rad) * Math.sin(dLo / 2) ** 2;
  return 2 * R * Math.asin(Math.min(1, Math.sqrt(a)));
}

// smooth is a moving mean over ± halfWindow miles of the cumulative distance (sparse NaNs are skipped).
export function smooth(vals: number[], cum: number[], halfWindowMi: number): number[] {
  const out: number[] = new Array(vals.length);
  let lo = 0;
  let hi = 0;
  for (let i = 0; i < vals.length; i++) {
    while (cum[i] - cum[lo] > halfWindowMi) lo++;
    while (hi < vals.length - 1 && cum[hi + 1] - cum[i] <= halfWindowMi) hi++;
    let sum = 0;
    let n = 0;
    for (let j = lo; j <= hi; j++) {
      if (Number.isFinite(vals[j])) {
        sum += vals[j];
        n++;
      }
    }
    out[i] = n > 0 ? sum / n : NaN;
  }
  return out;
}

export type Series = { speed?: number[]; eff?: number[]; elev?: number[] };

// efficiencySeries is the drive's efficiency at each sample, in mi/kWh, from the battery's change against the odometer (pack side, the
// figure the app shows for the drive). Each sample looks at a window around it of at least 1 mi that grows until the battery has moved by
// about 0.4 kWh (the percent steps are coarse, so a short window would be noise) or it spans 6 mi. null when the drive does not qualify:
// under EFF_MIN_MILES, a battery change under EFF_MIN_SOC_CHANGE points, or no pack size / battery / odometer values to work from.
export function efficiencySeries(s: Sample[], packKWh: number | undefined, cum: number[]): number[] | null {
  if (!finite(packKWh) || packKWh <= 0 || s.length < 3) return null;
  const soc = s.map((p) => p.soc);
  const odo = s.map((p) => p.odo);
  if (soc.some((v) => !finite(v)) || odo.some((v) => !finite(v))) return null;
  const miles = (odo[odo.length - 1] as number) - (odo[0] as number);
  const socChange = Math.abs((soc[0] as number) - (soc[soc.length - 1] as number));
  if (!(miles >= EFF_MIN_MILES) || !(socChange >= EFF_MIN_SOC_CHANGE)) return null;
  const MIN_WINDOW_MI = 1;
  const MAX_WINDOW_MI = 6;
  const MIN_KWH = 0.4;
  const CAP = 9; // mi/kWh: regen or a downhill can make the window's energy tiny or negative
  const out: number[] = new Array(s.length);
  for (let i = 0; i < s.length; i++) {
    let lo = i;
    let hi = i;
    for (;;) {
      const span = (odo[hi] as number) - (odo[lo] as number);
      const kwh = (((soc[lo] as number) - (soc[hi] as number)) / 100) * packKWh;
      if ((span >= MIN_WINDOW_MI && Math.abs(kwh) >= MIN_KWH) || span >= MAX_WINDOW_MI) break;
      const canLo = lo > 0;
      const canHi = hi < s.length - 1;
      if (!canLo && !canHi) break;
      // Grow on the side that is nearer in distance, so the window stays centred.
      if (canLo && (!canHi || cum[i] - cum[lo - 1] <= cum[hi + 1] - cum[i])) lo--;
      else hi++;
    }
    const span = (odo[hi] as number) - (odo[lo] as number);
    const kwh = (((soc[lo] as number) - (soc[hi] as number)) / 100) * packKWh;
    out[i] = kwh > 0.05 && span > 0 ? Math.min(CAP, span / kwh) : span > 0 ? CAP : NaN;
  }
  // Fill any gap (a window with no movement) from its neighbours.
  for (let i = 0; i < out.length; i++) {
    if (!Number.isFinite(out[i])) {
      let j = i - 1;
      while (j >= 0 && !Number.isFinite(out[j])) j--;
      let k = i + 1;
      while (k < out.length && !Number.isFinite(out[k])) k++;
      out[i] = j >= 0 ? out[j] : k < out.length ? out[k] : CAP;
    }
  }
  return smooth(out, cum, 0.5);
}

export function elevationSeries(s: Sample[], cum: number[]): number[] | null {
  if (s.length < 3) return null;
  const have = s.filter((p) => finite(p.alt)).length;
  if (have / s.length < ELEV_MIN_COVERAGE) return null;
  return smooth(s.map((p) => (finite(p.alt) ? p.alt : NaN)), cum, 0.4);
}

export function speedSeries(s: Sample[], cum: number[]): number[] | null {
  if (!s.some((p) => finite(p.s))) return null;
  return smooth(s.map((p) => (finite(p.s) ? p.s : NaN)), cum, 0.15);
}

// seriesFor builds every mode the drive qualifies for. The mode list is in display order; speed comes first when it exists.
export function seriesFor(s: Sample[], packKWh?: number): { series: Series; modes: Mode[]; cum: number[] } {
  const cum = cumulativeMiles(s);
  const series: Series = {};
  const speed = speedSeries(s, cum);
  if (speed) series.speed = speed;
  const eff = efficiencySeries(s, packKWh, cum);
  if (eff) series.eff = eff;
  const elev = elevationSeries(s, cum);
  if (elev) series.elev = elev;
  const modes = (["speed", "eff", "elev"] as Mode[]).filter((m) => series[m]);
  return { series, modes, cum };
}

const hex = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const mix = (a: string, b: string, t: number) =>
  "#" +
  hex(a)
    .map((v, i) => Math.round(v + (hex(b)[i] - v) * t).toString(16).padStart(2, "0"))
    .join("");

// rampColor is the colour at position t (0..1) along a ramp's stops.
export function rampColor(stops: string[], t: number): string {
  const n = stops.length - 1;
  const x = Math.min(0.9999, Math.max(0, t)) * n;
  const i = Math.floor(x);
  return mix(stops[i], stops[i + 1], x - i);
}

// gradientStops are the [progress, colour, ...] pairs of a line-gradient expression: the value at each of `n` evenly spaced points of the
// track's length (interpolated from the per-vertex `vals` over the vertices' cumulative distance), clamped to [lo, hi] and coloured by the
// ramp. Progress is strictly increasing, as MapLibre requires.
export function gradientStops(trackCum: number[], vals: number[], lo: number, hi: number, ramp: string[], n = 90): (number | string)[] {
  const total = trackCum[trackCum.length - 1];
  if (!(total > 0) || vals.length === 0) {
    const c = rampColor(ramp, 0.5);
    return [0, c, 1, c];
  }
  const out: (number | string)[] = [];
  let last = -1;
  let j = 0;
  for (let k = 0; k <= n; k++) {
    const p = k / n;
    const target = p * total;
    while (j < trackCum.length - 2 && trackCum[j + 1] < target) j++;
    const a = trackCum[j];
    const b = trackCum[j + 1] ?? a;
    const f = b > a ? Math.min(1, Math.max(0, (target - a) / (b - a))) : 0;
    const va = vals[j];
    const vb = vals[j + 1] ?? va;
    const v = Number.isFinite(va) && Number.isFinite(vb) ? va + (vb - va) * f : Number.isFinite(va) ? va : vb;
    const prog = Math.max(last + 1e-6, p);
    last = prog;
    out.push(prog, rampColor(ramp, ((Number.isFinite(v) ? v : lo) - lo) / (hi - lo)));
  }
  return out;
}

// The legend's CSS gradient for a ramp.
export function rampCSS(ramp: string[]): string {
  return `linear-gradient(90deg, ${ramp.join(", ")})`;
}

export type DistUnit = "mi" | "km";
export const MODE_LABEL: Record<Mode, string> = { speed: "Speed", eff: "Efficiency", elev: "Elevation" };
// The legend's title: efficiency says where it comes from, so it is not taken for Rivian's own figure.
export const MODE_TITLE: Record<Mode, string> = { speed: "Speed", eff: "Efficiency (from battery change)", elev: "Elevation" };

const MI_PER_KM = 0.6213711922;
const FT_PER_M = 3.28084;

// formatValue is a mode's value in the account's units, without a unit when `unit` is false (a legend's low end).
export function formatValue(mode: Mode, v: number, dist: DistUnit, withUnit = true): string {
  if (!Number.isFinite(v)) return "—";
  if (mode === "speed") {
    const n = Math.round(dist === "mi" ? v : v / MI_PER_KM);
    return withUnit ? `${n} ${dist === "mi" ? "mph" : "km/h"}` : String(n);
  }
  if (mode === "eff") {
    // v is mi/kWh; in km units it is km/kWh.
    const n = (dist === "mi" ? v : v / MI_PER_KM).toFixed(1);
    return withUnit ? `${n} ${dist}/kWh` : n;
  }
  const n = Math.round(dist === "mi" ? v * FT_PER_M : v).toLocaleString("en-US");
  return withUnit ? `${n} ${dist === "mi" ? "ft" : "m"}` : n;
}

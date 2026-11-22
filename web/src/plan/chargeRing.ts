// plan_v2: the charging-stop marker, "charge ring": one drawing for the map pin and the itinerary rail. A disc in the surface colour, a track ring, an
// amber arc from the arrival charge to the departure charge (0% at 12 o'clock, clockwise) and a bolt. Colours are CSS variables (styles/next-plan.css),
// so a theme switch repaints both without redrawing.

const VIEW = 30;
const STROKE = 3.5;
const R = (VIEW - STROKE) / 2;
const C = 2 * Math.PI * R;

// A battery percentage as a fraction of the ring, or null when it is not a number.
const frac = (pct: number | null | undefined): number | null => (typeof pct === "number" && Number.isFinite(pct) ? Math.max(0, Math.min(100, pct)) / 100 : null);

// The arc's length and where it starts, as fractions of the ring; null when there is nothing to draw (unknown, or no charge added).
export function ringArc(arrival: number | null | undefined, departure: number | null | undefined): { start: number; length: number } | null {
  const a = frac(arrival);
  const d = frac(departure);
  if (a == null || d == null || d <= a) return null;
  return { start: a, length: d - a };
}

// "Charging stop 1 of 2, 25 to 63 percent": the order is the label, as the number used to be.
export function chargeRingLabel(order: number, total: number, arrival: number | null | undefined, departure: number | null | undefined): string {
  const a = frac(arrival);
  const d = frac(departure);
  const range = a != null && d != null ? `, ${Math.round(a * 100)} to ${Math.round(d * 100)} percent` : "";
  return `Charging stop ${order} of ${total}${range}`;
}

// The drawing's numbers, shared by the markup below (the map's DOM marker) and the React component (the itinerary's rail), so the two cannot drift.
export const RING = { view: VIEW, stroke: STROKE, r: R, c: C, mid: VIEW / 2, bolt: "M16.6 7.6 10.6 16.2h4.1l-1.3 6.2 6-8.6h-4.1z" };

// The marker as SVG markup (built from numbers only). `size` is the pixel size; the drawing scales.
export function chargeRingSvg(size: number, arrival: number | null | undefined, departure: number | null | undefined): string {
  const arc = ringArc(arrival, departure);
  const mid = VIEW / 2;
  const arcEl = arc
    ? `<circle cx="${mid}" cy="${mid}" r="${R}" fill="none" stroke="var(--cr-arc)" stroke-width="${STROKE}" stroke-linecap="round" stroke-dasharray="${(arc.length * C).toFixed(2)} ${C.toFixed(2)}" transform="rotate(${(arc.start * 360 - 90).toFixed(2)} ${mid} ${mid})"/>`
    : "";
  return (
    `<svg width="${size}" height="${size}" viewBox="0 0 ${VIEW} ${VIEW}" aria-hidden="true" focusable="false" style="display:block">` +
    `<circle cx="${mid}" cy="${mid}" r="${mid}" fill="var(--cr-disc)"/>` +
    `<circle cx="${mid}" cy="${mid}" r="${R}" fill="none" stroke="var(--cr-track)" stroke-width="${STROKE}"/>` +
    arcEl +
    `<path d="${RING.bolt}" fill="var(--cr-arc)"/>` +
    `</svg>`
  );
}

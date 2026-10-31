// A plan's route coloured by leg: the stretch between two charging stops is one colour, the next stretch the next. A plan has no per-sample
// metric (no speed, battery or altitude along it), so the colour says where the stops split the trip, not how the trip goes. Pure: no map
// library and no DOM.
import { haversineMi, rampColor, RAMPS, type MapFlavor } from "./metrics";
import type { LatLng } from "./route";

export type Leg = { index: number; miles: number; color: string };

// cumulativeMiles: distance along the path at each vertex.
function cumulative(path: LatLng[]): number[] {
  const out = [0];
  for (let i = 1; i < path.length; i++) out.push(out[i - 1] + haversineMi(path[i - 1][0], path[i - 1][1], path[i][0], path[i][1]));
  return out;
}

// splitIndexes are the path vertices nearest each stop, in order along the path: each search starts where the last one ended, so a route that
// passes near an earlier stop again does not send a later stop backwards. Stops beyond the ends clamp to them.
export function splitIndexes(path: LatLng[], stops: LatLng[]): number[] {
  const out: number[] = [];
  let from = 0;
  for (const [la, lo] of stops) {
    let best = from;
    let bestD = Infinity;
    for (let i = from; i < path.length; i++) {
      const dy = path[i][0] - la;
      const dx = (path[i][1] - lo) * Math.cos((la * Math.PI) / 180);
      const d = dx * dx + dy * dy;
      if (d < bestD) {
        bestD = d;
        best = i;
      }
    }
    out.push(best);
    from = best;
  }
  return out;
}

// legColors gives n legs distinct colours along the speed ramp (blue → teal → yellow) of the flavour, so they read as a sequence.
export function legColors(n: number, flavor: MapFlavor): string[] {
  if (n <= 1) return [RAMPS[flavor].speed[1]];
  return Array.from({ length: n }, (_, i) => rampColor(RAMPS[flavor].speed, i / (n - 1)));
}

// legsOf splits the path at the stops (the plan's waypoints between origin and destination) into legs with their length and colour.
export function legsOf(path: LatLng[], stops: LatLng[], flavor: MapFlavor): { legs: Leg[]; stops: (number | string)[] } {
  const cum = cumulative(path);
  const total = cum[cum.length - 1] || 0;
  const cuts = splitIndexes(path, stops);
  // Drop a cut that adds nothing (at an end, or on top of the previous one): it would make an empty leg.
  const edges: number[] = [0];
  for (const c of cuts) if (c > edges[edges.length - 1] && c < path.length - 1) edges.push(c);
  edges.push(path.length - 1);
  const colors = legColors(edges.length - 1, flavor);
  const legs: Leg[] = [];
  for (let i = 0; i < edges.length - 1; i++) legs.push({ index: i, miles: cum[edges[i + 1]] - cum[edges[i]], color: colors[i] });
  return { legs, stops: legStops(legs, total) };
}

// legStops is the line-gradient expression for hard colour steps: each leg's colour from its start to just before the next leg's. Progress is
// strictly increasing, as MapLibre requires.
export function legStops(legs: Leg[], total: number): (number | string)[] {
  if (legs.length === 0 || !(total > 0)) {
    const c = legs[0]?.color ?? "#34d399";
    return [0, c, 1, c];
  }
  const EPS = 1e-6;
  const out: (number | string)[] = [];
  let at = 0;
  legs.forEach((l, i) => {
    const end = i === legs.length - 1 ? 1 : Math.min(1 - EPS * (legs.length - i), at + l.miles / total);
    out.push(i === 0 ? 0 : at + EPS, l.color, i === legs.length - 1 ? 1 : Math.max(at + 2 * EPS, end), l.color);
    at = end;
  });
  return out;
}

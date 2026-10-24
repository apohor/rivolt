// thinPath drops points from a route polyline so at most `max` remain, keeping the first and the last and spacing the
// rest evenly along the route. A drive of 1,000 km or more has tens of thousands of points; the charger corridor query
// needs a few thousand, and the thinned line follows the road to within a few hundred metres against a corridor of
// kilometres. The server thins too (it takes up to 200,000 points); sending fewer keeps the request small.
export function thinPath(path: [number, number][], max = 4000): [number, number][] {
  if (max < 3 || path.length <= max) return path;
  const R = 6371000;
  const rad = Math.PI / 180;
  const cum: number[] = new Array(path.length).fill(0);
  for (let i = 1; i < path.length; i++) {
    const [la1, lo1] = path[i - 1];
    const [la2, lo2] = path[i];
    const dLat = (la2 - la1) * rad;
    const dLon = (lo2 - lo1) * rad * Math.cos(((la1 + la2) / 2) * rad);
    const d = R * Math.hypot(dLat, dLon);
    if (!Number.isFinite(d)) return path; // a coordinate that is not a number: let the server refuse it
    cum[i] = cum[i - 1] + d;
  }
  const total = cum[cum.length - 1];
  const out: [number, number][] = [path[0]];
  if (total > 0) {
    const step = total / (max - 2);
    let next = step;
    for (let i = 1; i < path.length - 1; i++) {
      if (cum[i] >= next) {
        out.push(path[i]);
        next = cum[i] + step;
      }
    }
  }
  out.push(path[path.length - 1]);
  return out;
}

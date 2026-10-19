// decodePolyline decodes a Google-format encoded polyline into
// [lat, lon] pairs. Matches the encoder in
// internal/rivian/polyline.go on the backend at the default
// precision. precision defaults to 5 (Google encoded polyline);
// pass 6 for Valhalla shapes. Returns an empty array for empty / nullish /
// malformed input so callers can do a simple length check and
// fall back to a straight start→end line.
export function decodePolyline(
  s: string | undefined | null,
  precision: 5 | 6 = 5,
): [number, number][] {
  if (!s) return [];
  const factor = precision === 6 ? 1e-6 : 1e-5;
  const out: [number, number][] = [];
  let lat = 0;
  let lon = 0;
  let i = 0;
  const n = s.length;
  while (i < n) {
    let result = 0;
    let shift = 0;
    let b: number;
    do {
      if (i >= n) return out;
      b = s.charCodeAt(i++) - 63;
      result |= (b & 0x1f) << shift;
      shift += 5;
    } while (b >= 0x20);
    const dlat = (result & 1) !== 0 ? ~(result >> 1) : result >> 1;
    lat += dlat;

    result = 0;
    shift = 0;
    do {
      if (i >= n) return out;
      b = s.charCodeAt(i++) - 63;
      result |= (b & 0x1f) << shift;
      shift += 5;
    } while (b >= 0x20);
    const dlon = (result & 1) !== 0 ? ~(result >> 1) : result >> 1;
    lon += dlon;

    out.push([lat * factor, lon * factor]);
  }
  return out;
}

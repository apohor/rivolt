import L from "leaflet";

// safeFitBounds fits the map to `bounds`, but never while the container
// has no size. Leaflet measures the container at construction; when the
// wrapping card hasn't finished layout (Chromium, lazy chunks) the size
// is 0×0 and fitBounds computes a NaN centre, throwing
// "Invalid LatLng (NaN, NaN)". In that case it centres on the bounds
// instead and returns false so the caller can refit once the container
// has a size.
export function safeFitBounds(
  map: L.Map,
  bounds: L.LatLngBounds,
  options?: L.FitBoundsOptions,
): boolean {
  if (!bounds.isValid()) return false;
  const size = map.getSize();
  if (!(size.x > 0 && size.y > 0)) {
    map.setView(bounds.getCenter(), map.getZoom() || 10, { animate: false });
    return false;
  }
  map.fitBounds(bounds, options);
  return true;
}

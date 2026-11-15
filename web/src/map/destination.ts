// The live destination on the map: the planned route as a dashed line under the trail, and the destination's pin. Both are redrawn from the
// latest answer and removed when there is none; nothing about them is kept.
import type { GeoJSONSource, Map as MLMap } from "maplibre-gl";
import type { LatLng } from "./route";

export const DESTINATION_SOURCE = "rivolt-destination";
export const DESTINATION_LAYER = "rivolt-destination-route";
const ROUTE_COLOR = "#38bdf8";

// setDestinationRoute draws (or, with fewer than two points, removes) the planned route; beforeId is the layer it goes under (the trail's).
export function setDestinationRoute(map: MLMap, route: LatLng[], beforeId?: string): void {
  const data = {
    type: "FeatureCollection" as const,
    features: route.length >= 2 ? [{ type: "Feature" as const, geometry: { type: "LineString" as const, coordinates: route.map(([la, lo]) => [lo, la]) }, properties: {} }] : [],
  };
  const src = map.getSource(DESTINATION_SOURCE) as GeoJSONSource | undefined;
  if (src) src.setData(data as never);
  else map.addSource(DESTINATION_SOURCE, { type: "geojson", data: data as never });
  if (!map.getLayer(DESTINATION_LAYER)) {
    map.addLayer({ id: DESTINATION_LAYER, type: "line", source: DESTINATION_SOURCE, layout: { "line-cap": "butt", "line-join": "round" }, paint: { "line-color": ROUTE_COLOR, "line-width": 3.5, "line-opacity": 0.85, "line-dasharray": [2, 2] } }, beforeId);
  }
}

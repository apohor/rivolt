// The live destination on the map: the planned route under the trail, drawn like the trip planner's route (one colour, glow, casing), and the destination's pin. Both are redrawn from the
// latest answer and removed when there is none; nothing about them is kept.
import type { GeoJSONSource, Map as MLMap } from "maplibre-gl";
import { CASING, type LatLng } from "./route";
import { RAMPS } from "./metrics";

export const DESTINATION_SOURCE = "rivolt-destination";
export const DESTINATION_LAYER = "rivolt-destination-route";
const LAYERS = ["rivolt-destination-glow", "rivolt-destination-casing", DESTINATION_LAYER];

// setDestinationRoute draws (or, with fewer than two points, removes) the planned route; beforeId is the layer it goes under (the trail's).
export function setDestinationRoute(map: MLMap, route: LatLng[], flavor: "dark" | "light", beforeId?: string): void {
  const data = {
    type: "FeatureCollection" as const,
    features: route.length >= 2 ? [{ type: "Feature" as const, geometry: { type: "LineString" as const, coordinates: route.map(([la, lo]) => [lo, la]) }, properties: {} }] : [],
  };
  const src = map.getSource(DESTINATION_SOURCE) as GeoJSONSource | undefined;
  if (src) src.setData(data as never);
  else map.addSource(DESTINATION_SOURCE, { type: "geojson", data: data as never });
  if (!map.getLayer(DESTINATION_LAYER)) {
    // The widths, glow and casing of the planner's line (setDriveRoute), in its colour.
    const color = RAMPS[flavor].speed[1];
    const round = { "line-cap": "round", "line-join": "round" } as const;
    map.addLayer({ id: LAYERS[0], type: "line", source: DESTINATION_SOURCE, layout: round, paint: { "line-color": color, "line-width": 14, "line-blur": 9, "line-opacity": flavor === "dark" ? 0.5 : 0.3 } }, beforeId);
    map.addLayer({ id: LAYERS[1], type: "line", source: DESTINATION_SOURCE, layout: round, paint: { "line-color": CASING[flavor], "line-width": 8, "line-opacity": 0.92 } }, beforeId);
    map.addLayer({ id: LAYERS[2], type: "line", source: DESTINATION_SOURCE, layout: round, paint: { "line-color": color, "line-width": 4.6 } }, beforeId);
  }
}

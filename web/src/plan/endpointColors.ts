// The colours of a trip's two ends, in one place: the map's start and destination pins (and their name pills) and, under plan_v2, the itinerary's
// start and end dots and the planner form's From and To dots all read these.
export const START_COLOR = "#10b981"; // green
export const END_COLOR = "#f43f5e"; // rose

/** A round trip draws one pin on the map, in the start's colour: its end takes the same. */
export function endpointColor(role: "start" | "end", roundTrip = false): string {
  return role === "start" || roundTrip ? START_COLOR : END_COLOR;
}

/** The glow a dot carries in the form and itinerary: a soft ring and a halo in the dot's own colour. */
export function endpointGlow(color: string): string {
  return `0 0 0 3px ${color}2e, 0 0 10px 1px ${color}80`;
}

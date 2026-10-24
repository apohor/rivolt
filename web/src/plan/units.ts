import type { DistanceUnit } from "../lib/preferences";

// The plan's distances come in metres. They show in the unit the person chose (Settings), in whole units:
// "181 mi" or "291 km". (The stays line uses formatDistance, which also rounds to 5 mi or 10 km on long ones; a plan's own
// figures keep every whole unit so legs still add up on the page.)
export const distanceIn = (meters: number, unit: DistanceUnit): number => (unit === "mi" ? meters / 1609.344 : meters / 1000);
export const wholeDistance = (meters: number, unit: DistanceUnit): string => `${distanceIn(meters, unit).toFixed(0)} ${unit}`;

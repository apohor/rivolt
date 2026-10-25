// The drive map's speed bands (mph): the palette, and which of them a drive actually uses. Gray crawl → cyan city → emerald
// suburban → amber highway → rose interstate, with thresholds at common speed-limit boundaries. The legend names a band by its lower
// bound ("<5", "5", "25", "50", "65+") and shows only the bands on the map, so it stays a small corner chip.
export type SpeedBucket = { max: number; color: string; label: string; range: string };

export const SPEED_BUCKETS: SpeedBucket[] = [
  { max: 5, color: "#6b7280", label: "<5", range: "under 5 mph" },
  { max: 25, color: "#06b6d4", label: "5", range: "5 to 25 mph" },
  { max: 50, color: "#34d399", label: "25", range: "25 to 50 mph" },
  { max: 65, color: "#f59e0b", label: "50", range: "50 to 65 mph" },
  { max: Infinity, color: "#f43f5e", label: "65+", range: "65 mph and over" },
];

export function speedBucketIndex(mph: number): number {
  for (let i = 0; i < SPEED_BUCKETS.length; i++) if (mph < SPEED_BUCKETS[i].max) return i;
  return SPEED_BUCKETS.length - 1;
}

// The bands a drive's speeds fall in, slowest first. Unknown speeds count for none.
export function usedSpeedBuckets(speeds: (number | undefined | null)[]): SpeedBucket[] {
  const used = new Set<number>();
  for (const s of speeds) if (s != null && Number.isFinite(s)) used.add(speedBucketIndex(s));
  return [...used].sort((a, b) => a - b).map((i) => SPEED_BUCKETS[i]);
}

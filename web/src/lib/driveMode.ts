// The drive mode as the truck's app words it.
export function formatDriveMode(s: string): string {
  const map: Record<string, string> = {
    everyday: "All-Purpose",
    sport: "Sport",
    distance: "Conserve",
    winter: "Snow",
    towing: "Towing",
    off_road_auto: "All-Terrain",
    off_road_sand: "Soft Sand",
    off_road_rocks: "Rock Crawl",
    off_road_sport_auto: "Rally",
    off_road_sport_drift: "Drift",
  };
  return map[s] ?? (s ? s.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase()) : "—");
}

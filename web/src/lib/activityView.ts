// Activity's view switcher (ui_theme_next): the same period and All/Drives/
// Charges filter shown as the day-grouped feed, as tables, or on maps.
// URL state is ?view=feed|table|map; the feed is the default and carries no
// param, and every other param (the demo's tab=activity, f=, ...) is kept.

export type ActivityView = "feed" | "table" | "map" | "efficiency";

export const ACTIVITY_VIEWS: { key: ActivityView; label: string }[] = [
  { key: "feed", label: "Feed" },
  { key: "table", label: "Table" },
  { key: "map", label: "Map" },
  { key: "efficiency", label: "Efficiency" },
];

export function parseActivityView(raw: string | null): ActivityView {
  return raw === "table" || raw === "map" || raw === "efficiency" ? raw : "feed";
}

export function withActivityView(params: URLSearchParams, view: ActivityView): URLSearchParams {
  const next = new URLSearchParams(params);
  if (view === "feed") next.delete("view");
  else next.set("view", view);
  return next;
}

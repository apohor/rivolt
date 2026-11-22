// Where the Pro sheet opens from: a fixed list, the same as ProFromValues in internal/metrics/metrics.go. The sheet sends it with the
// pro_sheet_open event and in the checkout body, for counts only (the server counts any other value as "other").
export const PRO_FROM = ["settings", "truck", "old_record", "money", "vehicle", "planner", "saved_trips", "lapse_banner", "header", "menu", "activity_end"] as const;
export type ProFrom = (typeof PRO_FROM)[number];

import type { ProFeature } from "./proLimit";

// The entry point of a limit notice: the feature it names, else the record the free window hides.
export function proFromFeature(f?: ProFeature): ProFrom {
  switch (f) {
    case "planner":
    case "plan-vs-actual":
      return "planner";
    case "saved-trips":
      return "saved_trips";
    case "vehicle":
      return "vehicle";
    default:
      return "old_record";
  }
}

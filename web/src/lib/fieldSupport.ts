import type { Vehicle } from "./api";

// What /api/vehicles says about each live field on a model (field_support): the UI labels and hides
// from this one map and never checks which model it is. A server that sends no map (or no entry for a
// field) means the field is reported.
export type FieldLevel = "reported" | "estimate" | "best_effort" | "unavailable";

export function fieldLevel(v: Pick<Vehicle, "field_support"> | undefined, key: string): FieldLevel {
  return v?.field_support?.[key] ?? "reported";
}

// Hidden rows: a field the truck does not send, or that has not been confirmed.
export const fieldHidden = (v: Pick<Vehicle, "field_support"> | undefined, key: string): boolean =>
  fieldLevel(v, key) === "unavailable";

// "Time to full" or "Time to full (estimate)".
export function fieldLabel(v: Pick<Vehicle, "field_support"> | undefined, key: string, label: string): string {
  return fieldLevel(v, key) === "estimate" ? `${label} (estimate)` : label;
}

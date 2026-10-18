import type { RestoreProgress } from "./api";

// The restore stream's first event ("start") carries no counts, so a progress
// value may be partial. These helpers keep "undefined" out of the screen.
type Seen = Partial<RestoreProgress> | null | undefined;

const n = (v: number | undefined): number => (typeof v === "number" ? v : 0);

/** Rows written so far, or null before the first counted event. */
export function restoreRows(p: Seen): { drives: number; charges: number; samples: number } | null {
  if (!p || typeof p.samples !== "number") return null;
  return { drives: n(p.drives), charges: n(p.charges), samples: n(p.samples) };
}

/** " 12 drives, 3 charges, 4000 samples so far." once counts exist, else "". */
export function restoreSoFar(p: Seen): string {
  const r = restoreRows(p);
  return r ? ` ${r.drives} drives, ${r.charges} charges, ${r.samples} samples so far.` : "";
}

/** The "may be partly restored" note, only when something was written. */
export function restorePartialNote(p: Seen): string {
  const r = restoreRows(p);
  if (!r || r.drives + r.charges + r.samples === 0) return "";
  return ` It may be partly restored (${r.samples} samples so far); running it again is safe.`;
}

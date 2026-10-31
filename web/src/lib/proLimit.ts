// A free account's limits answer HTTP 402 with a code (never 403, which means signed-out or terms-required on both clients): act on
// the code. outside_free_window carries free_days; pro_required has no more. Bodies: ios/RivoltTests/Fixtures/err_*.json.

import { ApiError } from "./api";

// The 402 body: the error code, and the window in days for outside_free_window.
export type ProLimitBody = { error: string; free_days?: number };

// pro_required may also name the free limit that was hit (the saved-trip cap).
export type ProLimit = { code: "outside_free_window"; freeDays: number } | { code: "pro_required"; limit?: number };

export function proLimitOf(e: unknown): ProLimit | null {
  if (!(e instanceof ApiError) || e.status !== 402) return null;
  const b = e.body as { error?: unknown; free_days?: unknown } | string | null | undefined;
  const o = typeof b === "object" && b ? b : null;
  if (o?.error === "outside_free_window") return { code: "outside_free_window", freeDays: typeof o.free_days === "number" && o.free_days > 0 ? o.free_days : 90 };
  if (o?.error === "pro_required") return { code: "pro_required", ...(typeof (o as { limit?: unknown }).limit === "number" ? { limit: (o as { limit: number }).limit } : {}) };
  return null;
}

// A query's retry rule that never retries a free account's limit (HTTP 402 is an answer, not a failure) and otherwise retries up to `max`
// times, as react-query's default does for 3.
export const retryUnlessLimit = (max = 3) => (failureCount: number, error: unknown): boolean => !proLimitOf(error) && failureCount < max;

// Which free-plan limit a screen hit, for its own sentence. Each says what stays free and that nothing is lost.
export type ProFeature = "planner" | "plan-vs-actual" | "saved-trips" | "vehicle";

export function proFeatureText(f: ProFeature, limit?: number): string {
  switch (f) {
    case "planner":
      return "Multi-day plans, overnights and re-plan from here are part of Rivolt Pro. A single-day plan stays free.";
    case "plan-vs-actual":
      return "Plan vs actual is part of Rivolt Pro. Your saved trips and drives are kept either way.";
    case "saved-trips":
      return `The free plan keeps ${limit ?? 3} saved trips. Your other trips stay saved: you can open and delete them, and Pro lets you edit and add more.`;
    case "vehicle":
      return "This vehicle's history is part of Rivolt Pro. It keeps recording and alerting, and everything stays in your export. The free plan shows your primary vehicle.";
  }
}

export function proLimitText(l: ProLimit): string {
  return l.code === "outside_free_window"
    ? `This is older than the last ${l.freeDays} days that the free plan shows. It is kept, and it is in your export; Pro shows all of it.`
    : "This is part of Rivolt Pro. Everything you recorded is kept either way.";
}

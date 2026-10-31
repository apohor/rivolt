// A free account's limits answer HTTP 402 with a code (never 403, which means signed-out or terms-required on both clients): act on
// the code. outside_free_window carries free_days; pro_required has no more. Bodies: ios/RivoltTests/Fixtures/err_*.json.

import { ApiError } from "./api";

// The 402 body: the error code, and the window in days for outside_free_window.
export type ProLimitBody = { error: string; free_days?: number };

export type ProLimit = { code: "outside_free_window"; freeDays: number } | { code: "pro_required" };

export function proLimitOf(e: unknown): ProLimit | null {
  if (!(e instanceof ApiError) || e.status !== 402) return null;
  const b = e.body as { error?: unknown; free_days?: unknown } | string | null | undefined;
  const o = typeof b === "object" && b ? b : null;
  if (o?.error === "outside_free_window") return { code: "outside_free_window", freeDays: typeof o.free_days === "number" && o.free_days > 0 ? o.free_days : 90 };
  if (o?.error === "pro_required") return { code: "pro_required" };
  return null;
}

export function proLimitText(l: ProLimit): string {
  return l.code === "outside_free_window"
    ? `This is older than the last ${l.freeDays} days that the free plan shows. It is kept, and it is in your export; Pro shows all of it.`
    : "This is part of Rivolt Pro. Everything you recorded is kept either way.";
}

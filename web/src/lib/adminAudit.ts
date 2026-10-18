// Presentation helpers for the admin audit log: readable action names and a compact summary of an
// entry's details. The server never records secrets in details, but values are still length-capped.
import type { AdminAuditRow } from "./api";

export const AUDIT_ACTIONS: { id: string; label: string }[] = [
  { id: "user.create", label: "Create user" },
  { id: "user.set_role", label: "Change role" },
  { id: "user.set_disabled", label: "Disable or enable user" },
  { id: "user.delete", label: "Delete user" },
  { id: "user.sync_rivian", label: "Sync Rivian" },
  { id: "user.refresh_rivian_session", label: "Refresh Rivian session" },
  { id: "signup_request.approve", label: "Approve signup" },
  { id: "signup_request.reject", label: "Reject signup" },
  { id: "flag.kill_switch", label: "Kill switch" },
  { id: "flag.trip_planner", label: "Trip planner flag" },
  { id: "flag.money_tab", label: "Money tab flag" },
  { id: "flag.statement_email", label: "Statement email flag" },
  { id: "flag.signup_cap", label: "Signup cap" },
  { id: "flag.ai_call_cap", label: "AI call cap" },
  { id: "settings.ai", label: "AI settings" },
  { id: "settings.recap", label: "Recap settings" },
  { id: "settings.gps", label: "GPS settings" },
  { id: "idp_failure.retry", label: "Retry sign-in removal" },
  { id: "impersonation.start", label: "Start view-as" },
  { id: "impersonation.stop", label: "Stop view-as" },
];

export function actionLabel(action: string): string {
  const known = AUDIT_ACTIONS.find((a) => a.id === action);
  if (known) return known.label;
  const t = action.replace(/[._]/g, " ");
  return t.charAt(0).toUpperCase() + t.slice(1);
}

const MAX = 80;
function short(v: unknown): string {
  const s = typeof v === "string" ? v : JSON.stringify(v);
  return s.length > MAX ? `${s.slice(0, MAX - 1)}…` : s;
}

// "paused: false → true", "limit: 25", "fields: a, b": one short string per key.
export function detailPairs(details: Record<string, unknown> | null | undefined): string[] {
  if (!details) return [];
  const keys = Object.keys(details).sort();
  const out: string[] = [];
  for (const k of keys) {
    if (k.endsWith("_before") || k.startsWith("before")) continue;
    const v = details[k];
    const beforeKey = [`${k}_before`, `before_${k}`].find((b) => b in details);
    if (beforeKey) out.push(`${k}: ${short(details[beforeKey])} → ${short(v)}`);
    else if (Array.isArray(v) && v.every((x) => typeof x === "string")) out.push(`${k}: ${(v as string[]).join(", ")}`);
    else out.push(`${k}: ${short(v)}`);
  }
  return out;
}

export type OutcomeTone = "ok" | "bad" | "unknown";
export function outcome(r: Pick<AdminAuditRow, "outcome" | "status">): { label: string; tone: OutcomeTone } {
  if (r.outcome == null) return { label: "no result recorded", tone: "unknown" };
  const ok = r.outcome === "ok" || r.outcome === "success" || (r.status !== undefined && r.status >= 200 && r.status < 400);
  return { label: r.status ? `${r.outcome} (${r.status})` : r.outcome, tone: ok ? "ok" : "bad" };
}

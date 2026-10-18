// The admin home's "is everything OK?" logic: the response of GET /api/admin/health-summary
// (internal/adminhealth) and the rules that turn it into an overall verdict. Each section can be
// "unavailable" on its own; that is shown as a gap, never as healthy.
export type Section = { status: "ok" | "unavailable"; reason?: string };
export type HealthSummary = {
  generated_at: string;
  version: string;
  alerts: Section & { items: { name: string; severity: string; since: string }[] };
  kill_switch: Section & { paused: boolean };
  rivian: Section & {
    window: string;
    results: Record<string, number>;
    error_rate: number | null;
    breaker_state: number | null; // 0 closed, 1 half open, 2 open (worst across pods)
    breaker_trips: Record<string, number>;
    rate_limited: Record<string, number>;
  };
  vehicles: Section & { leased: number; streaming: number };
  disk: Section & { used_pct: number | null };
  backup: Section & { age_seconds: number | null };
  // Counts only, from rows the product already keeps. Absent on a server that predates it.
  engagement?: Section & {
    active_7d: number;
    active_28d: number;
    connected_no_data: number;
    first_data_7d: number;
    first_data_28d: number;
    median_seconds_to_first_data: number | null;
    emails_sent: Record<string, number>;
  };
};

export type Tone = "ok" | "warn" | "bad" | "unknown";
export type Check = { id: string; label: string; tone: Tone; detail: string };

const HOUR = 3600;
// Thresholds, in one place.
export const T = {
  errorWarn: 0.1,
  errorBad: 0.3,
  diskWarn: 85,
  diskBad: 95,
  backupWarnH: 26, // a daily backup is late after a day plus slack
  backupBadH: 48,
};

export function formatAge(seconds: number): string {
  if (seconds < 90) return `${Math.round(seconds)}s`;
  if (seconds < 90 * 60) return `${Math.round(seconds / 60)}m`;
  if (seconds < 48 * HOUR) return `${Math.round(seconds / HOUR)}h`;
  return `${Math.round(seconds / (24 * HOUR))}d`;
}

export function checks(s: HealthSummary): Check[] {
  const out: Check[] = [];

  if (s.alerts.status !== "ok") out.push({ id: "alerts", label: "Alerts", tone: "unknown", detail: s.alerts.reason || "unavailable" });
  else if (s.alerts.items.length === 0) out.push({ id: "alerts", label: "Alerts", tone: "ok", detail: "none firing" });
  else {
    const critical = s.alerts.items.some((a) => a.severity === "critical" || a.severity === "page");
    out.push({ id: "alerts", label: "Alerts", tone: critical ? "bad" : "warn", detail: `${s.alerts.items.length} firing` });
  }

  if (s.kill_switch.status !== "ok") out.push({ id: "kill", label: "Kill switch", tone: "unknown", detail: s.kill_switch.reason || "unavailable" });
  else out.push({ id: "kill", label: "Kill switch", tone: s.kill_switch.paused ? "bad" : "ok", detail: s.kill_switch.paused ? "Rivian calls are paused" : "calls flowing" });

  const r = s.rivian;
  if (r.status !== "ok") out.push({ id: "rivian", label: "Rivian calls", tone: "unknown", detail: r.reason || "unavailable" });
  else {
    const open = r.breaker_state === 2;
    const half = r.breaker_state === 1;
    const rate = r.error_rate;
    let tone: Tone = "ok";
    if (open || (rate != null && rate >= T.errorBad)) tone = "bad";
    else if (half || (rate != null && rate >= T.errorWarn)) tone = "warn";
    const detail = open ? "breaker open" : half ? "breaker half open" : rate == null ? "no calls in the window" : `${(rate * 100).toFixed(1)}% errors`;
    out.push({ id: "rivian", label: "Rivian calls", tone, detail });
  }

  const v = s.vehicles;
  if (v.status !== "ok") out.push({ id: "vehicles", label: "Vehicles", tone: "unknown", detail: v.reason || "unavailable" });
  else {
    const tone: Tone = v.leased === 0 || v.streaming >= v.leased ? "ok" : v.streaming === 0 ? "bad" : "warn";
    out.push({ id: "vehicles", label: "Vehicles", tone, detail: `${v.streaming} streaming of ${v.leased} leased` });
  }

  const d = s.disk;
  if (d.status !== "ok" || d.used_pct == null) out.push({ id: "disk", label: "Database disk", tone: "unknown", detail: d.reason || "unavailable" });
  else out.push({ id: "disk", label: "Database disk", tone: d.used_pct >= T.diskBad ? "bad" : d.used_pct >= T.diskWarn ? "warn" : "ok", detail: `${d.used_pct.toFixed(0)}% used` });

  const b = s.backup;
  if (b.status !== "ok" || b.age_seconds == null) out.push({ id: "backup", label: "Backup", tone: "unknown", detail: b.reason || "unavailable" });
  else {
    const h = b.age_seconds / HOUR;
    out.push({ id: "backup", label: "Backup", tone: h >= T.backupBadH ? "bad" : h >= T.backupWarnH ? "warn" : "ok", detail: `last ${formatAge(b.age_seconds)} ago` });
  }
  return out;
}

export type Verdict = { tone: Tone; headline: string };

// Bad beats warn beats unknown beats ok; "unknown" (a section could not be read) never reads as healthy.
export function verdict(cs: Check[]): Verdict {
  const bad = cs.filter((c) => c.tone === "bad");
  const warn = cs.filter((c) => c.tone === "warn");
  const unknown = cs.filter((c) => c.tone === "unknown");
  if (bad.length) return { tone: "bad", headline: `${bad.length + warn.length} need${bad.length + warn.length === 1 ? "s" : ""} attention: ${[...bad, ...warn].map((c) => c.label).join(", ")}` };
  if (warn.length) return { tone: "warn", headline: `${warn.length} need${warn.length === 1 ? "s" : ""} a look: ${warn.map((c) => c.label).join(", ")}` };
  if (unknown.length) {
    const detail = unknown.length === cs.length ? "no check could be read" : `${unknown.map((c) => c.label).join(", ")} could not be read`;
    return { tone: "unknown", headline: `Nothing wrong found, but ${detail}` };
  }
  return { tone: "ok", headline: "Everything looks OK" };
}

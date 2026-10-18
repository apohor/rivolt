import { describe, expect, it } from "vitest";
import { checks, formatAge, verdict, type HealthSummary } from "./adminHealth";

const ok = { status: "ok" as const };
const base = (): HealthSummary => ({
  generated_at: "2026-10-18T12:00:00Z", version: "0.36.20",
  alerts: { ...ok, items: [] },
  kill_switch: { ...ok, paused: false },
  rivian: { ...ok, window: "24h", results: { success: 1000 }, error_rate: 0.01, breaker_state: 0, breaker_trips: {}, rate_limited: {} },
  vehicles: { ...ok, leased: 5, streaming: 5 },
  disk: { ...ok, used_pct: 40 },
  backup: { ...ok, age_seconds: 5 * 3600 },
});
const tone = (s: HealthSummary, id: string) => checks(s).find((c) => c.id === id)!.tone;
const unavailable = (reason = "not configured") => ({ status: "unavailable" as const, reason });

describe("health checks", () => {
  it("everything fine reads OK", () => {
    expect(verdict(checks(base()))).toEqual({ tone: "ok", headline: "Everything looks OK" });
  });

  it("alerts: any firing warns, a critical one is bad", () => {
    const s = base();
    s.alerts.items = [{ name: "RivolRivianErrors", severity: "warning", since: "2026-10-18T10:00:00Z" }];
    expect(tone(s, "alerts")).toBe("warn");
    s.alerts.items.push({ name: "DBDown", severity: "critical", since: "2026-10-18T11:00:00Z" });
    expect(tone(s, "alerts")).toBe("bad");
  });

  it("a paused kill switch is bad", () => {
    const s = base();
    s.kill_switch.paused = true;
    expect(tone(s, "kill")).toBe("bad");
  });

  it("Rivian: the breaker and the error rate decide", () => {
    const s = base();
    s.rivian.error_rate = 0.12;
    expect(tone(s, "rivian")).toBe("warn");
    s.rivian.error_rate = 0.4;
    expect(tone(s, "rivian")).toBe("bad");
    s.rivian.error_rate = 0.01;
    s.rivian.breaker_state = 1;
    expect(tone(s, "rivian")).toBe("warn");
    s.rivian.breaker_state = 2;
    expect(tone(s, "rivian")).toBe("bad");
    s.rivian.breaker_state = 0;
    s.rivian.error_rate = null;
    expect(checks(s).find((c) => c.id === "rivian")!.detail).toBe("no calls in the window");
    expect(tone(s, "rivian")).toBe("ok");
  });

  it("vehicles: streaming below leased warns, none streaming is bad, nothing leased is fine", () => {
    const s = base();
    s.vehicles.streaming = 3;
    expect(tone(s, "vehicles")).toBe("warn");
    s.vehicles.streaming = 0;
    expect(tone(s, "vehicles")).toBe("bad");
    s.vehicles.leased = 0;
    expect(tone(s, "vehicles")).toBe("ok");
  });

  it("disk and backup thresholds", () => {
    const s = base();
    s.disk.used_pct = 86;
    expect(tone(s, "disk")).toBe("warn");
    s.disk.used_pct = 96;
    expect(tone(s, "disk")).toBe("bad");
    s.backup.age_seconds = 30 * 3600;
    expect(tone(s, "backup")).toBe("warn");
    s.backup.age_seconds = 50 * 3600;
    expect(tone(s, "backup")).toBe("bad");
  });

  it("an unavailable section is unknown, never healthy", () => {
    const s = base();
    s.alerts = { ...unavailable("alertmanager unreachable"), items: [] };
    s.disk = { ...unavailable(), used_pct: null };
    expect(tone(s, "alerts")).toBe("unknown");
    expect(checks(s).find((c) => c.id === "alerts")!.detail).toBe("alertmanager unreachable");
    const v = verdict(checks(s));
    expect(v.tone).toBe("unknown");
    expect(v.headline).toContain("Alerts, Database disk could not be read");
  });

  it("when nothing at all could be read, it says so plainly", () => {
    const s = base();
    s.alerts = { ...unavailable(), items: [] };
    s.kill_switch = { ...unavailable(), paused: false };
    s.rivian = { ...s.rivian, ...unavailable() };
    s.vehicles = { ...unavailable(), leased: 0, streaming: 0 };
    s.disk = { ...unavailable(), used_pct: null };
    s.backup = { ...unavailable(), age_seconds: null };
    expect(verdict(checks(s))).toEqual({ tone: "unknown", headline: "Nothing wrong found, but no check could be read" });
  });

  it("bad outranks warn outranks unknown, and the headline names them", () => {
    const s = base();
    s.kill_switch.paused = true;
    s.disk.used_pct = 90;
    s.backup = { ...unavailable(), age_seconds: null };
    const v = verdict(checks(s));
    expect(v.tone).toBe("bad");
    expect(v.headline).toBe("2 need attention: Kill switch, Database disk");
  });
});

describe("formatAge", () => {
  it("picks the unit", () => {
    expect(formatAge(30)).toBe("30s");
    expect(formatAge(600)).toBe("10m");
    expect(formatAge(5 * 3600)).toBe("5h");
    expect(formatAge(72 * 3600)).toBe("3d");
  });
});

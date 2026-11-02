import { describe, expect, it } from "vitest";
import type { DayActivity } from "./api";
import { lowestTire, otaNowLine, otaStep, otaStepLine, rivianSaysLow, sleepSummary, softwareInfo, tireWheels } from "./truckHealth";

describe("lowestTire", () => {
  it("ignores unreported wheels and flags a low reading", () => {
    const r = lowestTire({ tire_pressure_fl_bar: 0, tire_pressure_fr_bar: 2.1, tire_pressure_rl_bar: 0, tire_pressure_rr_bar: 2.9 });
    expect(r?.bar).toBe(2.1);
    expect(r?.low).toBe(true);
    expect(r?.psi).toBeCloseTo(30.5, 0);
  });
  it("is null when no wheel reported", () => {
    expect(lowestTire({ tire_pressure_fl_bar: 0, tire_pressure_fr_bar: 0, tire_pressure_rl_bar: 0, tire_pressure_rr_bar: 0 })).toBeNull();
  });
});

describe("softwareInfo", () => {
  it("hides when absent and only offers an update that differs", () => {
    expect(softwareInfo({ ota_current_version: "", ota_available_version: "" })).toBeNull();
    expect(softwareInfo({ ota_current_version: "2026.12", ota_available_version: "2026.12" })?.available).toBeNull();
    expect(softwareInfo({ ota_current_version: "2026.12", ota_available_version: "2026.14" })?.available).toBe("2026.14");
  });
});

describe("sleepSummary", () => {
  const day = (d: string, idle: number, sleeps: number, failed: number): DayActivity => ({
    day: `${d}T00:00:00Z`, asleep_h: 10, idle_awake_h: idle, sleeps, failed_sleeps: failed,
  });
  it("converts idle hours to minutes and flags trouble over two days", () => {
    const s = sleepSummary([day("2026-10-01", 1, 4, 0), day("2026-10-02", 2, 8, 2), day("2026-10-03", 0.5, 6, 2)]);
    expect(s.bars[1].value).toBe(120);
    expect(s.trouble).toEqual({ failed: 4, sleeps: 14 });
  });
  it("is empty and quiet without data", () => {
    const s = sleepSummary([]);
    expect(s.daysWithData).toBe(0);
    expect(s.trouble).toBeNull();
  });

  it("averages completed days only, leaving out the partial today", () => {
    const now = new Date("2026-10-03T08:00:00Z");
    const s = sleepSummary([day("2026-10-01", 2, 0, 0), day("2026-10-02", 4, 0, 0), day("2026-10-03", 0.1, 0, 0)], 3, now);
    expect(s.avgDays).toBe(2);
    expect(s.avgIdleMinutes).toBe(180);
  });
});

describe("softwareInfo update line", () => {
  it("hides an unset or zero available version", () => {
    for (const v of ["", "0.0.0", "0.0", "0", "  ", undefined as unknown as string]) {
      expect(softwareInfo({ ota_current_version: "2026.36.0", ota_available_version: v })?.available).toBeNull();
    }
  });
  it("shows a real newer version and hides one equal to current", () => {
    expect(softwareInfo({ ota_current_version: "2026.36.0", ota_available_version: "2026.40.1" })?.available).toBe("2026.40.1");
    expect(softwareInfo({ ota_current_version: "2026.36.0", ota_available_version: "2026.36.0" })?.available).toBeNull();
  });
});

describe("tireWheels", () => {
  const base = { tire_pressure_fl_bar: 2.9, tire_pressure_fr_bar: 2.9, tire_pressure_rl_bar: 2.8, tire_pressure_rr_bar: 2.8, tire_pressure_status_fl: "Normal", tire_pressure_status_fr: "Normal", tire_pressure_status_rl: "Normal", tire_pressure_status_rr: "Normal" };
  it("lists the four wheels front row first, in psi", () => {
    const w = tireWheels(base)!;
    expect(w.map((x) => x.label)).toEqual(["Front left", "Front right", "Rear left", "Rear right"]);
    expect(Math.round(w[0].psi!)).toBe(42);
  });
  it("marks low only below the 32 psi floor or when Rivian says so, never calls a wheel OK", () => {
    const w = tireWheels({ ...base, tire_pressure_fl_bar: 2.1, tire_pressure_rr_bar: 2.7, tire_pressure_status_rr: "Warning_Soft" })!;
    expect(w.map((x) => [x.low, x.rivianLow])).toEqual([[true, false], [false, false], [false, false], [false, true]]);
    // 38 psi is under a placard but above the floor: the number only, no claim either way.
    expect(tireWheels({ ...base, tire_pressure_fl_bar: 2.62 })![0]).toMatchObject({ low: false, rivianLow: false });
  });
  it("a wheel with no reading has no number and no floor claim; none at all hides the card", () => {
    const w = tireWheels({ ...base, tire_pressure_fl_bar: 0 })!;
    expect(w[0]).toMatchObject({ bar: null, psi: null, low: false });
    expect(tireWheels({ ...base, tire_pressure_fl_bar: 0, tire_pressure_fr_bar: 0, tire_pressure_rl_bar: 0, tire_pressure_rr_bar: 0 })).toBeNull();
  });
  it("reads only a fixed list of Rivian's low words", () => {
    for (const v of ["warning_soft", "Warning_Soft", " WARNING_HARD ", "warning puncture", "warning-hard", "WARNING_SOFT", "TIRE_PRESSURE_STATUS_WARNING_SOFT", "tire_pressure_status_warning_hard"]) expect(rivianSaysLow(v)).toBe(true);
    for (const v of ["", "Normal", "unknown", "undefined", "high", "lowish", "UNSPECIFIED", "unspecified", "NORMAL", "low", "critical", "very low", "TIRE_PRESSURE_STATUS_NORMAL", "TIRE_PRESSURE_STATUS_UNSPECIFIED"]) expect(rivianSaysLow(v)).toBe(false);
  });
});

describe("otaStep (the server's ota.step)", () => {
  it("names the ten known steps in plain words", () => {
    const words: Record<string, string> = {
      ready_to_download: "Ready to download", downloading: "Downloading", preparing: "Preparing", ready_to_install: "Ready to install",
      scheduled_to_install: "Install scheduled", install_countdown: "Install about to start", awaiting_install: "Waiting to install",
      installing: "Installing", install_success: "Installed", connection_lost: "Update connection lost",
    };
    for (const [step, word] of Object.entries(words)) expect(otaStep({ step })?.word).toBe(word);
  });
  it("shows nothing for unknown, an unrecognised word, or no step", () => {
    for (const step of ["unknown", "download_failed", "Installing", "", undefined]) expect(otaStep({ step })).toBeNull();
    expect(otaStep(undefined)).toBeNull();
    expect(otaStep({ install_progress: 50 })).toBeNull();
  });
  it("shows a number only while installing, and 0 is a start, not nothing", () => {
    expect(otaStepLine(otaStep({ step: "installing", install_progress: 37 })!)).toBe("Installing 37%");
    expect(otaStep({ step: "installing", install_progress: 0 })?.pct).toBe(0);
    expect(otaStep({ step: "installing" })?.pct).toBeNull();
    expect(otaStep({ step: "downloading", install_progress: 40 })?.pct).toBeNull();
    expect(otaStep({ step: "installing", install_progress: 140 })?.pct).toBe(100);
  });
  it("is never inferred: 100% is not Installed, and install_success is", () => {
    expect(otaStep({ step: "installing", install_progress: 100 })?.word).toBe("Installing");
    expect(otaStep({ step: "install_success" })?.word).toBe("Installed");
  });
  it("puts the pending version first when there is one", () => {
    expect(otaStepLine(otaStep({ step: "downloading", available: { version: "2026.38.0" } })!)).toBe("2026.38.0 · Downloading");
    expect(otaStepLine(otaStep({ step: "downloading", available: { version: "0.0.0" } })!)).toBe("Downloading");
  });
});

describe("otaNowLine", () => {
  it("is Now's installing line, with the version and the number when there are ones", () => {
    expect(otaNowLine(otaStep({ step: "installing", install_progress: 37, available: { version: "2026.38.0" } })!)).toBe("Installing software 2026.38.0 · 37%");
    expect(otaNowLine(otaStep({ step: "installing" })!)).toBe("Installing software");
    expect(otaNowLine(otaStep({ step: "installing", install_progress: 0 })!)).toBe("Installing software · 0%");
  });
});

import { describe, expect, it } from "vitest";
import { formatDistance } from "./preferences";

describe("formatDistance", () => {
  it("converts km to miles with privacy-safe rounding", () => {
    expect(formatDistance(412.3, "mi")).toBe("255 mi");
    expect(formatDistance(100, "mi")).toBe("60 mi");
    expect(formatDistance(30, "mi")).toBe("19 mi");
    expect(formatDistance(0.4, "mi")).toBe("0 mi");
  });
  it("keeps the km rounding", () => {
    expect(formatDistance(412.3, "km")).toBe("410 km");
    expect(formatDistance(37.6, "km")).toBe("38 km");
  });
  it("shows one decimal under 10 only when fine", () => {
    expect(formatDistance(4.26, "km", { fine: true })).toBe("4.3 km");
    expect(formatDistance(4.26, "mi", { fine: true })).toBe("2.6 mi");
    expect(formatDistance(4.26, "km")).toBe("4 km");
  });
  it("handles missing values", () => {
    expect(formatDistance(null, "mi")).toBe("—");
  });
});

describe("temperature unit default", () => {
  it("follows the distance unit until a unit is picked", async () => {
    const { defaultTemperatureUnit } = await import("./preferences");
    expect(defaultTemperatureUnit("mi")).toBe("f");
    expect(defaultTemperatureUnit("km")).toBe("c");
  });

  async function fresh(stored?: object) {
    const { vi } = await import("vitest");
    vi.resetModules();
    window.localStorage.clear();
    if (stored) window.localStorage.setItem("rivolt.preferences.v1", JSON.stringify(stored));
    return import("./preferences");
  }

  it("a new user in miles gets °F, and km gives °C", async () => {
    let m = await fresh();
    expect(m.usePreferences).toBeDefined();
    m.setDistanceUnit("km");
    expect(JSON.parse(window.localStorage.getItem("rivolt.preferences.v1") as string).temperatureUnit).toBe("c");
    m.setDistanceUnit("mi");
    expect(JSON.parse(window.localStorage.getItem("rivolt.preferences.v1") as string).temperatureUnit).toBe("f");
    m = await fresh();
    m.setTemperatureUnit("c");
    m.setDistanceUnit("mi");
    expect(JSON.parse(window.localStorage.getItem("rivolt.preferences.v1") as string).temperatureUnit).toBe("c");
  });

  it("preferences saved before this keep their unit", async () => {
    const m = await fresh({ temperatureUnit: "c", distanceUnit: "mi" });
    m.setDistanceUnit("km");
    m.setDistanceUnit("mi");
    expect(JSON.parse(window.localStorage.getItem("rivolt.preferences.v1") as string).temperatureUnit).toBe("c");
  });
});

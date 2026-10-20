import { describe, expect, it } from "vitest";
import { fmtDuration, priceMonth, readSampleTrip, safeDemoUrl, safeSignupUrl, readSampleTrips, routePath } from "./sampleTrip";

describe("routePath", () => {
  it("fits the route inside the box and keeps the ends", () => {
    const r = routePath([[34, -118], [35, -117], [36, -115]], 600, 360, 16);
    expect(r).not.toBeNull();
    expect(r!.d.startsWith("M")).toBe(true);
    for (const [x, y] of [r!.start, r!.end]) {
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThanOrEqual(600);
      expect(y).toBeGreaterThanOrEqual(0);
      expect(y).toBeLessThanOrEqual(360);
    }
    // North is up: the first point (south) is lower on the page than the last.
    expect(r!.start[1]).toBeGreaterThan(r!.end[1]);
  });
  it("is null without a route", () => {
    expect(routePath([])).toBeNull();
    expect(routePath([[1, 1]])).toBeNull();
  });
});

describe("page JSON", () => {
  it("reads nothing when the script is missing or broken", () => {
    document.body.innerHTML = '<script type="application/json" id="sample-trip">{nope</script>';
    expect(readSampleTrip()).toBeNull();
    expect(readSampleTrips()).toBeNull();
    document.body.innerHTML = "";
  });
  it("reads the hub list", () => {
    document.body.innerHTML = '<script type="application/json" id="sample-trips">{"trips":[{"slug":"a","title":"A","intro":"i","totals":{}}]}</script>';
    expect(readSampleTrips()?.[0].slug).toBe("a");
    document.body.innerHTML = "";
  });
});

describe("fmtDuration", () => {
  it("formats minutes", () => {
    expect(fmtDuration(36)).toBe("36 min");
    expect(fmtDuration(375)).toBe("6 h 15 min");
  });
});

describe("server-supplied links", () => {
  it("only lets the demo host and /signup through", () => {
    expect(safeDemoUrl("https://demo.rivolt.dev/trips/plan?preset=x")).not.toBeNull();
    expect(safeDemoUrl("/demo/trips/plan?preset=x")).not.toBeNull();
    for (const bad of ["javascript:alert(1)", "https://evil.example/", "https://demo.rivolt.dev.evil.example/", "//evil.example"]) expect(safeDemoUrl(bad)).toBeNull();
    expect(safeSignupUrl("/signup")).toBe("/signup");
    expect(safeSignupUrl("https://evil.example/signup")).toBe("/signup");
  });
});

describe("priceMonth", () => {
  it("formats a YYYY-MM month and ignores anything else", () => {
    expect(priceMonth("2026-10")).toBe("Oct 2026");
    expect(priceMonth("")).toBe("");
    expect(priceMonth("soon")).toBe("");
    expect(priceMonth(undefined)).toBe("");
  });
});

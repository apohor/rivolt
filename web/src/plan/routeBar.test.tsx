import { afterEach, describe, expect, it } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { formatResultSummaryParts, formatRouteSummary, geocodePlace, useRouteBarCollapse, useRouteBarState, type RouteEnd } from "./routeBar";

afterEach(cleanup);

const LA: RouteEnd = { lat: 34.05, lon: -118.24, label: "Los Angeles" };
const LV: RouteEnd = { lat: 36.17, lon: -115.14, label: "Las Vegas" };

describe("useRouteBarState", () => {
  it("starts empty and can plan once both ends are set", () => {
    const { result } = renderHook(() => useRouteBarState());
    expect(result.current).toMatchObject({ origin: null, destination: null, departureAt: "", startingSoc: "", canPlan: false });
    act(() => result.current.setOrigin(LA));
    expect(result.current.canPlan).toBe(false);
    act(() => result.current.setDestination(LV));
    expect(result.current.canPlan).toBe(true);
    act(() => result.current.setOrigin(null));
    expect(result.current.canPlan).toBe(false);
  });

  it("swap exchanges the ends exactly once, also with one end missing, and twice restores", () => {
    const { result } = renderHook(() => useRouteBarState());
    act(() => {
      result.current.setOrigin(LA);
      result.current.setDestination(LV);
    });
    act(() => result.current.swap());
    expect(result.current.origin).toBe(LV);
    expect(result.current.destination).toBe(LA);
    act(() => result.current.swap());
    expect(result.current.origin).toBe(LA);
    expect(result.current.destination).toBe(LV);
    act(() => result.current.setDestination(null));
    act(() => result.current.swap());
    expect(result.current.origin).toBeNull();
    expect(result.current.destination).toBe(LA);
  });

  it("keeps departure and SoC apart from the ends, and the setters are stable", () => {
    const { result, rerender } = renderHook(() => useRouteBarState());
    const first = result.current;
    act(() => {
      result.current.setDepartureAt("2026-11-14T08:00");
      result.current.setStartingSoc("85");
    });
    act(() => result.current.swap());
    expect(result.current.departureAt).toBe("2026-11-14T08:00");
    expect(result.current.startingSoc).toBe("85");
    rerender();
    expect(result.current.setOrigin).toBe(first.setOrigin);
    expect(result.current.swap).toBe(first.swap);
  });
});

describe("formatRouteSummary", () => {
  it("shows the ends, the SoC and the departure when present", () => {
    expect(formatRouteSummary({ origin: LA, destination: LV, startingSoc: "", departureAt: "" })).toBe("Los Angeles → Las Vegas");
    expect(formatRouteSummary({ origin: LA, destination: LV, startingSoc: " 85 ", departureAt: "" })).toBe("Los Angeles → Las Vegas · 85%");
    const s = formatRouteSummary({ origin: LA, destination: LV, startingSoc: "85", departureAt: "2026-11-14T08:00" }, "en-US");
    expect(s).toBe("Los Angeles → Las Vegas · 85% · Sat 8:00 AM");
  });
  it("marks missing ends and ignores an unparseable departure", () => {
    expect(formatRouteSummary({ origin: null, destination: null, startingSoc: "", departureAt: "" })).toBe("… → …");
    expect(formatRouteSummary({ origin: LA, destination: null, startingSoc: "", departureAt: "not a date" })).toBe("Los Angeles → …");
  });
});

describe("useRouteBarCollapse", () => {
  const A = { plan: "a" };
  const B = { plan: "b" };

  it("is open without a result and folds to the summary when one arrives", () => {
    const { result, rerender } = renderHook(({ has, key }) => useRouteBarCollapse(has, key), { initialProps: { has: false, key: null as unknown } });
    expect(result.current.collapsed).toBe(false);
    rerender({ has: true, key: A });
    expect(result.current.collapsed).toBe(true);
    expect(result.current.bodyId).toBeTruthy();
  });

  it("Edit opens it, Done folds it, and a NEW result folds it again", () => {
    const { result, rerender } = renderHook(({ has, key }) => useRouteBarCollapse(has, key), { initialProps: { has: true, key: A as unknown } });
    expect(result.current.collapsed).toBe(true);
    act(() => result.current.toggle());
    expect(result.current.collapsed).toBe(false);
    rerender({ has: true, key: A }); // same result: stays open for editing
    expect(result.current.collapsed).toBe(false);
    rerender({ has: true, key: B }); // replanned: back to the one-line summary
    expect(result.current.collapsed).toBe(true);
    act(() => result.current.toggle());
    act(() => result.current.toggle());
    expect(result.current.collapsed).toBe(true);
  });

  it("opens again when the result goes away", () => {
    const { result, rerender } = renderHook(({ has, key }) => useRouteBarCollapse(has, key), { initialProps: { has: true, key: A as unknown } });
    expect(result.current.collapsed).toBe(true);
    rerender({ has: false, key: null });
    expect(result.current.collapsed).toBe(false);
  });
});

describe("geocodePlace", () => {
  it("is the locality of a street hit, else the name unless that is a street", () => {
    expect(geocodePlace({ name: "Main St, Austin", locality: "Austin" })).toBe("Austin");
    expect(geocodePlace({ name: "Yosemite National Park" })).toBe("Yosemite National Park");
    expect(geocodePlace({ name: "12 Main St" })).toBeUndefined();
  });
});

describe("formatResultSummaryParts", () => {
  const base = { origin: LA, destination: LV, startingSoc: "", departureAt: "" };
  it("names the departure, 'Leaves now' when none, and the typed or live starting charge", () => {
    expect(formatResultSummaryParts(base, null).facts).toBe("Leaves now");
    expect(formatResultSummaryParts(base, 72.4).facts).toBe("Leaves now · 72% charge now");
    expect(formatResultSummaryParts({ ...base, startingSoc: "85" }, 72).facts).toBe("Leaves now · 85% charge");
    expect(formatResultSummaryParts({ ...base, departureAt: "2026-11-14T08:00" }, null, "en-US").facts).toMatch(/^Sat,? 8:00 AM$/);
  });
  it("shortens the places to the city and keeps the full labels for the title", () => {
    const long = { ...base, origin: { lat: 1, lon: 1, label: "Current vehicle position" }, destination: { lat: 2, lon: 2, label: "Sacramento, California, United States" } };
    const p = formatResultSummaryParts(long, 74);
    expect(p.route).toBe("Current vehicle position → Sacramento");
    expect(p.facts).toBe("Leaves now · 74% charge now");
    const addr = formatResultSummaryParts({ ...base, origin: { lat: 1, lon: 1, label: "1560 North, Salt Lake City, Utah, United States" }, destination: { lat: 2, lon: 2, label: "Las Vegas, Nevada, United States" } }, null);
    expect(addr.route).toBe("Salt Lake City → Las Vegas");
    expect(p.full).toBe("Current vehicle position → Sacramento, California, United States · Leaves now · 74% charge now");
  });
  it("names the place: an address by its city, a city, a national park, a saved label as it is", () => {
    const route = (origin: string, destination: string, place?: string) =>
      formatResultSummaryParts({ ...base, origin: { lat: 1, lon: 1, label: origin }, destination: { lat: 2, lon: 2, label: destination, place } }, null).route;
    expect(route("12 Main St, Austin, TX, United States", "Boise, Idaho, United States")).toBe("Austin → Boise");
    expect(route("Denver, Colorado, United States", "Yosemite National Park, California, United States")).toBe("Denver → Yosemite National Park");
    expect(route("St. George, Utah, United States", "St. Louis, Missouri, United States")).toBe("St. George → St. Louis");
    expect(route("Mt. Shasta, California, United States", "Fort Collins, Colorado, United States")).toBe("Mt. Shasta → Fort Collins");
    expect(route("Main St & 5th Ave, Austin, TX, United States", "St. Louis, Missouri, USA")).toBe("Austin → St. Louis");
    // A saved place keeps its own label, comma and all; a geocoder locality beats the street.
    expect(route("Denver, Colorado, United States", "Mom's house, Austin", "Mom's house, Austin")).toBe("Denver → Mom's house, Austin");
    expect(route("Denver, Colorado, United States", "12 Main St, Austin, TX, United States", "Austin")).toBe("Denver → Austin");
  });
});

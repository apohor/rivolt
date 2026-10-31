import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../lib/routeThumbs", () => {
  const known = new Map<string, string>();
  return {
    routeThumb: (id: string) => known.get(id),
    wantRouteThumb: vi.fn(),
    subscribeRouteThumbs: () => () => {},
    __known: known,
  };
});

import { RouteThumb, thumbPath } from "./RouteThumb";

describe("thumbPath", () => {
  it("fits the route in the box, ends included, without stretching it", () => {
    const wide = thumbPath([[10, 10], [10, 10.2]])!; // east-west only
    const tall = thumbPath([[10, 10], [10.2, 10]])!; // north-south only
    expect(wide.a[1]).toBeCloseTo(wide.b[1], 1); // a flat line: both ends at one height
    expect(tall.a[0]).toBeCloseTo(tall.b[0], 1); // a vertical line: both ends at one x
    for (const p of [wide.a, wide.b, tall.a, tall.b]) {
      expect(p[0]).toBeGreaterThanOrEqual(0);
      expect(p[0]).toBeLessThanOrEqual(64);
      expect(p[1]).toBeGreaterThanOrEqual(0);
      expect(p[1]).toBeLessThanOrEqual(40);
    }
    expect(wide.d.startsWith("M")).toBe(true);
  });
  it("draws north up", () => {
    const r = thumbPath([[10, 10], [10.1, 10.1]])!;
    expect(r.b[1]).toBeLessThan(r.a[1]); // the northern end is higher on the screen
  });
  it("has nothing to draw for under two points or no extent", () => {
    expect(thumbPath([])).toBeNull();
    expect(thumbPath([[10, 10]])).toBeNull();
    expect(thumbPath([[10, 10], [10, 10]])).toBeNull();
  });
});

describe("RouteThumb", () => {
  it("is an empty box until it has a route, so a row does not jump", () => {
    const { container } = render(<RouteThumb driveId="d1" />);
    const box = container.querySelector("[data-route-thumb]")!;
    expect(box.getAttribute("data-route-thumb")).toBe("empty");
    expect(box.querySelector("svg")).toBeNull();
    expect(box.getAttribute("aria-hidden")).toBe("true");
  });
});

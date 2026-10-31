import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const routeThumbs = vi.fn();
vi.mock("./api", () => ({ backend: { routeThumbs: (ids: string[]) => routeThumbs(ids) } }));

import { forgetRouteThumbs, routeThumb, subscribeRouteThumbs, wantRouteThumb } from "./routeThumbs";

beforeEach(() => {
  vi.useFakeTimers();
  forgetRouteThumbs();
  routeThumbs.mockReset();
});
afterEach(() => vi.useRealTimers());

describe("route thumbnail batching", () => {
  it("sends the asks of one moment as one request, not one per row", async () => {
    routeThumbs.mockResolvedValue({ a: "AAA", b: "BBB" });
    const seen = vi.fn();
    const off = subscribeRouteThumbs(seen);
    for (const id of ["a", "b", "a", "c"]) wantRouteThumb(id);
    await vi.advanceTimersByTimeAsync(100);
    expect(routeThumbs).toHaveBeenCalledTimes(1);
    expect(routeThumbs.mock.calls[0][0]).toEqual(["a", "b", "c"]);
    expect(routeThumb("a")).toBe("AAA");
    expect(routeThumb("c")).toBe(""); // asked, and the server had none: remembered, not asked again
    expect(seen).toHaveBeenCalled();
    off();
  });

  it("remembers answers: a row asking again costs nothing", async () => {
    routeThumbs.mockResolvedValue({ a: "AAA" });
    wantRouteThumb("a");
    await vi.advanceTimersByTimeAsync(100);
    wantRouteThumb("a");
    await vi.advanceTimersByTimeAsync(100);
    expect(routeThumbs).toHaveBeenCalledTimes(1);
  });

  it("splits more than 60 ids into requests of at most 60", async () => {
    routeThumbs.mockImplementation(async (ids: string[]) => Object.fromEntries(ids.map((i) => [i, "X"])));
    for (let i = 0; i < 130; i++) wantRouteThumb(`d${i}`);
    await vi.advanceTimersByTimeAsync(100);
    expect(routeThumbs.mock.calls.map((c) => c[0].length)).toEqual([60, 60, 10]);
  });

  it("a failed batch leaves its rows unknown, so they ask again when they next appear", async () => {
    routeThumbs.mockRejectedValueOnce(new Error("offline")).mockResolvedValue({ a: "AAA" });
    wantRouteThumb("a");
    await vi.advanceTimersByTimeAsync(100);
    expect(routeThumb("a")).toBeUndefined();
    wantRouteThumb("a");
    await vi.advanceTimersByTimeAsync(100);
    expect(routeThumb("a")).toBe("AAA");
  });
});

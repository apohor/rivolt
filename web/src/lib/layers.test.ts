import { afterEach, describe, expect, it, vi } from "vitest";
import { backend } from "./api";
import { findLayersAlongPath } from "./poi";

const path: [number, number][] = [[30, -97], [31, -97]];
afterEach(() => vi.restoreAllMocks());

describe("findLayersAlongPath", () => {
  it("asks for nothing when no layer is on", async () => {
    const spy = vi.spyOn(backend, "corridorLayers");
    expect(await findLayersAlongPath(path, [])).toEqual({ pois: [], total: 0 });
    expect(spy).not.toHaveBeenCalled();
  });
  it("sends the kinds, keeps the distance off the route and the total before the cap", async () => {
    const spy = vi.spyOn(backend, "corridorLayers").mockResolvedValue({ pois: [{ lat: 30.5, lon: -97, isDCFC: true, isL2: false, offKm: 3.4 }], total: 41 } as never);
    const r = await findLayersAlongPath(path, ["dcfc", "hotels"]);
    expect(spy.mock.calls[0][0].kinds).toEqual(["dcfc", "hotels"]);
    expect(r?.total).toBe(41);
    expect(r?.pois[0].offKm).toBe(3.4);
  });
});

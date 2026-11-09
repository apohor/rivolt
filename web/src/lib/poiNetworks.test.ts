import { afterEach, describe, expect, it, vi } from "vitest";

const { corridorLayers } = vi.hoisted(() => ({ corridorLayers: vi.fn() }));
vi.mock("./api", async (orig) => ({ ...(await orig<typeof import("./api")>()), backend: { corridorLayers } }));
vi.mock("./demo", () => ({ inDemo: () => false }));

import { ApiError } from "./api";
import { findChargersOfNetworks } from "./poi";

describe("findChargersOfNetworks", () => {
  afterEach(() => corridorLayers.mockReset());
  const path: [number, number][] = [[0, 0], [0, 1]];

  it("asks for fast chargers of just those networks", async () => {
    corridorLayers.mockResolvedValue({ pois: [{ lat: 1, lon: 2, name: "A", isDCFC: true, isL2: false, networkSlug: "ea" }], total: 1 });
    const r = await findChargersOfNetworks(path, ["ea", "ran"]);
    expect(corridorLayers.mock.calls[0][0]).toMatchObject({ kinds: ["dcfc"], networks: ["ea", "ran"] });
    expect(r?.map((p) => p.name)).toEqual(["A"]);
  });
  it("hands its abort signal to the request, so toggling a chip cancels the preferred-networks one too", async () => {
    corridorLayers.mockResolvedValue({ pois: [], total: 0 });
    const ac = new AbortController();
    await findChargersOfNetworks(path, ["ea"], ac.signal);
    expect(corridorLayers.mock.calls[0][1]).toBe(ac.signal);
  });
  it("asks nothing with no networks, and is null without an archive", async () => {
    expect(await findChargersOfNetworks(path, [])).toEqual([]);
    expect(corridorLayers).not.toHaveBeenCalled();
    corridorLayers.mockRejectedValue(new ApiError(404, null));
    expect(await findChargersOfNetworks(path, ["ea"])).toBeNull();
  });
});

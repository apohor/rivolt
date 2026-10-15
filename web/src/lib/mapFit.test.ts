import L from "leaflet";
import { describe, expect, it, vi } from "vitest";
import { safeFitBounds } from "./mapFit";

const bounds = L.latLngBounds([[40, -100], [41, -99]]);
const fakeMap = (x: number, y: number) =>
  ({
    getSize: () => ({ x, y }),
    getZoom: () => 0,
    setView: vi.fn(),
    fitBounds: vi.fn(),
  }) as unknown as L.Map & { setView: ReturnType<typeof vi.fn>; fitBounds: ReturnType<typeof vi.fn> };

describe("safeFitBounds", () => {
  it("0x0 at mount, then sized: fitBounds runs on the refit", () => {
    let size = { x: 0, y: 0 };
    const m = {
      getSize: () => size,
      getZoom: () => 0,
      setView: vi.fn(),
      fitBounds: vi.fn(),
    } as unknown as L.Map & { fitBounds: ReturnType<typeof vi.fn> };
    expect(safeFitBounds(m, bounds)).toBe(false);
    expect(m.fitBounds).not.toHaveBeenCalled();
    size = { x: 400, y: 300 };
    expect(safeFitBounds(m, bounds)).toBe(true);
    expect(m.fitBounds).toHaveBeenCalledWith(bounds, undefined);
  });
  it("centres instead of fitting when the container has no size", () => {
    const m = fakeMap(0, 0);
    expect(safeFitBounds(m, bounds)).toBe(false);
    expect(m.fitBounds).not.toHaveBeenCalled();
    expect(m.setView).toHaveBeenCalledOnce();
  });
  it("fits once the container has a size", () => {
    const m = fakeMap(300, 200);
    expect(safeFitBounds(m, bounds)).toBe(true);
    expect(m.fitBounds).toHaveBeenCalledOnce();
  });
});

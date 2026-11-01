import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { stubFetchRoutes } from "../test/fetchRoutes";

// Regression: the truck's modem hands back GPS fixes late and again, so a sample can repeat the previous fix or carry one OLDER than the one
// before it. Drawn in arrival order the route line jumps back along the road and forward again (a scribble on the gradient line). The drive
// page must hand DriveMap only fixes that move forward in fix time. Plainly fake places near (10, 10).
const seen: { lat: number; lon: number; t: number }[][] = [];
vi.mock("../map/DriveMap", () => ({
  DriveMap: ({ points }: { points: { lat: number; lon: number; t: number }[] }) => {
    seen.push(points);
    return null;
  },
}));
import DriveDetailPage from "./DriveDetailPage";

const T0 = Date.parse("2026-01-01T12:00:00Z");
const iso = (s: number) => new Date(T0 + s * 1000).toISOString();
// Sample i arrives at 5i s. Its fix is its own time, except that every 3rd sample repeats the previous fix and every 7th carries the fix of
// the sample four steps back (older than the newest): the position then is where the truck WAS, so the line would jump back.
const row = (i: number) => {
  let step = i;
  if (i % 7 === 0 && i > 4) step = i - 4;
  else if (i % 3 === 0 && i > 0) step = i - 1;
  return {
    VehicleID: "truck-A", At: iso(i * 5), Lat: 10 + step * 0.0004, Lon: 10 + step * 0.0004, SpeedMph: 40,
    ShiftState: "D", BatteryLevelPct: 60, RangeMi: 200, OdometerMi: 100, ChargingState: "", ChargerPowerKW: 0,
    ChargeLimitPct: 80, InsideTempC: 20, OutsideTempC: 20, DriveNumber: 1, ChargeNumber: 0, Source: "live",
    LocationFixAt: iso(step * 5),
  };
};
const rows = Array.from({ length: 60 }, (_, i) => row(i));
const drive = {
  ID: "d1", VehicleID: "truck-A", StartedAt: iso(0), EndedAt: iso(59 * 5), DistanceMi: 5, StartSoCPct: 70, EndSoCPct: 60,
  StartOdometerMi: 100, EndOdometerMi: 105, StartLat: 10, StartLon: 10, EndLat: 10.02, EndLon: 10.02,
  MaxSpeedMph: 60, AvgSpeedMph: 40, EnergyUsedKWh: 3, Source: "live",
};

class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
describe("DriveDetailPage route points, fix order", () => {
  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", RO);
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    seen.length = 0;
  });
  it("feeds the map only fixes that move forward: no repeats, nothing older than the newest", async () => {
    stubFetchRoutes({
      "/api/drives": { body: [drive] },
      "/api/samples": { body: rows },
      "/api/vehicles": { body: [] },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={["/drives/d1"]}>
          <Routes>
            <Route path="/drives/:id" element={<DriveDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(seen.some((p) => p.length > 0)).toBe(true));
    const pts = seen.filter((p) => p.length > 0).at(-1)!;
    // Fewer than the 60 samples (the repeats and the older fixes are gone), and the line only ever moves forward.
    expect(pts.length).toBeLessThan(60);
    expect(pts.length).toBeGreaterThan(30);
    for (let i = 1; i < pts.length; i++) {
      expect(pts[i].lat).toBeGreaterThan(pts[i - 1].lat);
      expect(pts[i].lon).toBeGreaterThan(pts[i - 1].lon);
    }
  });
});

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { stubFetchRoutes } from "../test/fetchRoutes";

// Regression: /api/samples is user-scoped, so an account with two trucks
// driving the same minutes got both tracks interleaved on the route map
// (a fan of chords between them). The drive page must hand DriveMap only
// its own vehicle's samples.
const seen: { lat: number; lon: number }[][] = [];
vi.mock("../map/DriveMap", () => ({
  DriveMap: ({ points }: { points: { lat: number; lon: number }[] }) => {
    seen.push(points);
    return null;
  },
}));
import DriveDetailPage from "./DriveDetailPage";

const T0 = Date.parse("2026-01-01T12:00:00Z");
const iso = (s: number) => new Date(T0 + s * 1000).toISOString();
// Truck A drives a line; truck B (same account) is ~11 km away in the same minutes.
const mk = (vid: string, lat0: number, lon0: number, i: number) => ({
  VehicleID: vid, At: iso(i * 5), Lat: lat0 + i * 0.0004, Lon: lon0 + i * 0.0004, SpeedMph: 40,
  ShiftState: "D", BatteryLevelPct: 60, RangeMi: 200, OdometerMi: 100, ChargingState: "", ChargerPowerKW: 0,
  ChargeLimitPct: 80, InsideTempC: 20, OutsideTempC: 20, DriveNumber: 1, ChargeNumber: 0, Source: "live",
  LocationFixAt: iso(i * 5),
});
const rows = Array.from({ length: 60 }, (_, i) => [mk("truck-A", 40.0, -100.0, i), mk("truck-B", 40.1, -100.1, i)]).flat();
const drive = {
  ID: "d1", VehicleID: "truck-A", StartedAt: iso(0), EndedAt: iso(59 * 5), DistanceMi: 5, StartSoCPct: 70, EndSoCPct: 60,
  StartOdometerMi: 100, EndOdometerMi: 105, StartLat: 40, StartLon: -100, EndLat: 40.02, EndLon: -99.98,
  MaxSpeedMph: 60, AvgSpeedMph: 40, EnergyUsedKWh: 3, Source: "live",
};

class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
describe("DriveDetailPage route points", () => {
  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", RO);
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    seen.length = 0;
  });
  it("feeds the map only the drive's own vehicle", async () => {
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
    // Only truck A's 60 samples, and no step is larger than the sample spacing.
    expect(pts).toHaveLength(60);
    let worst = 0;
    for (let i = 1; i < pts.length; i++) worst = Math.max(worst, Math.hypot(pts[i].lat - pts[i - 1].lat, pts[i].lon - pts[i - 1].lon));
    expect(worst).toBeLessThan(0.001);
  });
});

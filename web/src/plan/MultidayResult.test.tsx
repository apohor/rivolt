import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MultidayResult } from "./MultidayResult";

// The day card draws a map, which a unit test cannot: the header text is what is under test.
vi.mock("./RouteCard", () => ({ RouteCard: () => null }));
import type { TripPlanMultidayResponse } from "../lib/api";

const day = (index: number, depart_at: string) =>
  ({
    index,
    plan: {},
    depart_at,
    arrive_at: depart_at,
    departure_soc: 80,
    arrival_soc: 20,
    soc_below_limit: index === 2,
    ...(index === 1 ? { overnight: { name: "Des Moines", parked_hours: 11, l2_kw: 7, added_kwh: 52, post_charge_soc_pct: 80, capped: true } } : {}),
  }) as never;

const resp = (extra: Partial<TripPlanMultidayResponse>) =>
  ({
    days: [day(1, "2026-10-20T15:00:00Z"), day(2, "2026-10-21T15:00:00Z")],
    total: { distance_meters: 1000, drive_duration_sec: 3600, charging_duration_sec: 600 },
    ...extra,
  }) as TripPlanMultidayResponse;

describe("MultidayResult", () => {
  afterEach(cleanup);

  it("shows the Simulated badge, per-day warnings and a partial-failure banner", () => {
    render(<MultidayResult response={resp({ simulated: true, failed_day: 3, error: "no route" })} originLabel="A" destLabel="B" />);
    expect(screen.getByText("Simulated")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("Planned through day 2");
    expect(screen.getByText("The battery gets very low on this day.")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("Day 3 failed: no route. Fix day 3 and plan again; earlier days are kept.");
  });

  it("puts the overnight between the days with arrival, parked hours and the charge", () => {
    render(<MultidayResult response={resp({})} originLabel="A" destLabel="B" />);
    const row = document.querySelector("[data-overnight-row]")!;
    expect(row.textContent).toContain("Overnight at Des Moines");
    expect(row.textContent).toContain("Parked about 11 h");
    expect(row.textContent).toContain("Charging at 7 kW adds about 52 kWh");
    expect(row.textContent).toContain("(capped)");
  });

  it("shows no banner or badge for a clean plan", () => {
    render(<MultidayResult response={resp({})} originLabel="A" destLabel="B" />);
    expect(screen.queryByText("Simulated")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("MultidayResult charging stops", () => {
  const route = (types: string[]) => ({
    TotalDriveDistanceMeters: 100_000,
    TotalDriveDurationSec: 3600,
    TotalChargingDurationSec: 600,
    BatteryEmptyToDestMeters: 0,
    Waypoints: types.map((t) => ({ WaypointType: t, Name: t, Latitude: 1, Longitude: 2 })),
  });
  const withRoute = (types: string[]) =>
    ({
      days: [{ index: 1, plan: { Routes: [route(types)] }, depart_at: "2026-10-20T15:00:00Z", arrive_at: "2026-10-20T20:00:00Z", departure_soc: 90, arrival_soc: 30 }],
      total: { distance_meters: 1, drive_duration_sec: 1, charging_duration_sec: 1 },
    }) as unknown as TripPlanMultidayResponse;

  it("counts the planner's own charger type names, and only says none are needed when there are none", () => {
    const wrap = (ui: React.ReactElement) => <QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>;
    const { container } = render(wrap(<MultidayResult response={withRoute(["ORIGIN", "DC_CHARGE_STATION", "DESTINATION"])} originLabel="A" destLabel="B" />));
    expect(container.textContent).toContain("1 charging stop ·");
    expect(container.textContent).not.toContain("No charging stop needed");
    cleanup();
    const none = render(wrap(<MultidayResult response={withRoute(["ORIGIN", "DESTINATION"])} originLabel="A" destLabel="B" />));
    expect(none.container.textContent).toContain("0 charging stops");
    expect(none.container.textContent).toContain("No charging stop needed on this day");
  });
});

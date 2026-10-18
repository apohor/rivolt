import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { MultidayResult } from "./MultidayResult";
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

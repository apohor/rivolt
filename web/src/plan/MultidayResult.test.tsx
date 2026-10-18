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
    expect(screen.getByText("battery gets very low")).toBeTruthy();
  });

  it("shows no banner or badge for a clean plan", () => {
    render(<MultidayResult response={resp({})} originLabel="A" destLabel="B" />);
    expect(screen.queryByText("Simulated")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

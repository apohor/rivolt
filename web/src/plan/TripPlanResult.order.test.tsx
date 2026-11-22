import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TripPlanResult } from "./TripPlanResult";
import type { TripPlan } from "../lib/api";

vi.mock("./RouteCard", () => ({ RouteCard: () => <div data-testid="route-card" /> }));
vi.mock("./TripAdvice", () => ({ TripAdviceCard: () => <div data-testid="advice-card" /> }));
vi.mock("./OvernightSuggestions", () => ({ OvernightSuggestions: () => null }));

const plan = { Routes: [{ Waypoints: [] }], costs: [] } as unknown as TripPlan;
const order = () => screen.getAllByTestId(/route-card|advice-card/).map((e) => e.dataset.testid);

describe("TripPlanResult trip analysis position", () => {
  afterEach(cleanup);
  it("leads the result by default (key off)", () => {
    render(<TripPlanResult plan={plan} originLabel="A" destLabel="B" aiEnabled />);
    expect(order()).toEqual(["advice-card", "route-card"]);
  });
  it("follows the summary and itinerary with adviceLast (plan_v2)", () => {
    render(<TripPlanResult plan={plan} originLabel="A" destLabel="B" aiEnabled adviceLast />);
    expect(order()).toEqual(["route-card", "advice-card"]);
  });
});

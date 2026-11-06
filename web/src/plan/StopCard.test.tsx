import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { StopCard } from "./StopCard";
import type { PlannedWaypoint } from "../lib/api";

const w = { Name: "A place you chose", WaypointType: "waypoint", Source: "via", Latitude: 1, Longitude: 2, MaxPowerKW: 0, ChargeDurationSec: 0, ArrivalSoC: 0, DepartureSoC: 0, ArrivalReachableMeters: 0, DepartureReachableMeters: 0, AdapterRequired: false, EntityID: "" } as PlannedWaypoint;

describe("StopCard for your own stop", () => {
  afterEach(cleanup);
  it("is listed with the Your stop label and says the route only passes through", () => {
    render(<ol><StopCard kind="via" name={w.Name} waypoint={w} currency="USD" timeShiftMs={0} selected={false} onSelect={() => {}} showLeg={false} /></ol>);
    expect(screen.getByText("Your stop")).toBeInTheDocument();
    expect(screen.getByText("A place you chose")).toBeInTheDocument();
    expect(screen.getByText("The route passes through it")).toBeInTheDocument();
    expect(screen.queryByText(/min$/)).toBeNull();
  });
});

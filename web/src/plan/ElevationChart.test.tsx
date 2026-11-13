import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { ElevationChart } from "./ElevationChart";

afterEach(cleanup);
const PROFILE = { points: [[0, 100], [160934, 300], [321869, 100]] as [number, number][], waypoint_at_m: [0, 160934, 321869] };

function Harness({ unit = "mi" as "mi" | "km" }) {
  const [s, setS] = useState<number | null>(null);
  return <ElevationChart profile={PROFILE} share={s} onShare={setS} distanceUnit={unit} stopNames={["A", "Stop", "B"]} />;
}

describe("ElevationChart", () => {
  it("names the range and the context line, in the account's units", () => {
    render(<Harness />);
    expect(screen.getByRole("slider")).toHaveAttribute("aria-label", "Elevation along the route, 328 to 984 ft, over 200 mi");
    expect(screen.getByText(/Rivian's estimate decides the energy/)).toBeInTheDocument();
    cleanup();
    render(<Harness unit="km" />);
    expect(screen.getByRole("slider")).toHaveAttribute("aria-label", "Elevation along the route, 100 to 300 m, over 322 km");
  });

  it("moves with the arrow keys, Home, End and Escape", () => {
    render(<Harness />);
    const chart = screen.getByRole("slider");
    fireEvent.keyDown(chart, { key: "End" });
    expect(document.querySelector("[data-elevation-readout]")).toHaveTextContent("200 mi · 328 ft");
    fireEvent.keyDown(chart, { key: "Home" });
    expect(document.querySelector("[data-elevation-readout]")).toHaveTextContent("0 mi · 328 ft");
    fireEvent.keyDown(chart, { key: "ArrowRight" });
    expect(chart).toHaveAttribute("aria-valuenow", "2");
    fireEvent.keyDown(chart, { key: "Escape" });
    expect(document.querySelector("[data-elevation-cursor]")).toBeNull();
  });

  it("marks the charging stop", () => {
    render(<Harness />);
    expect(document.querySelectorAll("[data-elevation-stop]")).toHaveLength(1);
    expect(screen.getByText("Stop")).toBeInTheDocument();
  });
});

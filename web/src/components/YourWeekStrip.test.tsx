import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { Charge, Drive } from "../lib/api";
import { YourWeekStrip } from "./YourWeekStrip";

afterEach(cleanup);
const NOW = new Date(2026, 9, 28, 12, 0);
const at = (d: number, h = 9) => new Date(2026, 9, d, h, 0).toISOString();
const drive = (day: number, miles: number) => ({ StartedAt: at(day), EndedAt: at(day, 10), DistanceMi: miles }) as unknown as Drive;
const charge = (day: number, kwh: number, extra: object = {}) => ({ StartedAt: at(day, 18), EndedAt: at(day, 20), EnergyAddedKWh: kwh, Cost: 0, ...extra }) as unknown as Charge;

describe("Your week strip", () => {
  it("shows miles, energy, an estimated cost and the change, in plain words", () => {
    render(<YourWeekStrip now={NOW} drives={[drive(26, 120), drive(19, 100)]} charges={[charge(27, 51, { Cost: 18.4, Currency: "USD", cost_source_effective: "home_rate" })]} />);
    const strip = screen.getByRole("region", { name: "Your week" });
    expect(strip).toHaveTextContent("Your week");
    expect(strip.querySelector("[data-week-miles]")).toHaveTextContent("120 mi");
    expect(strip.querySelector("[data-week-kwh]")).toHaveTextContent("51 kWh added");
    expect(strip.querySelector("[data-week-cost]")).toHaveTextContent("$18.40 est. charging");
    expect(strip.querySelector("[data-week-change]")).toHaveTextContent("▲ 20% miles vs last week so far");
  });
  it("drops the cost with no priced charge and the change with no last week", () => {
    render(<YourWeekStrip now={NOW} drives={[drive(26, 120)]} charges={[charge(27, 51)]} />);
    expect(document.querySelector("[data-week-cost]")).toBeNull();
    expect(document.querySelector("[data-week-change]")).toBeNull();
  });
  it("is not there when nothing was recorded this week", () => {
    const { container } = render(<YourWeekStrip now={NOW} drives={[drive(19, 100)]} charges={[]} />);
    expect(container).toBeEmptyDOMElement();
  });
});

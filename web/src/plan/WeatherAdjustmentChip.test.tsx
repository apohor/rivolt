import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { setDistanceUnit, setTemperatureUnit } from "../lib/preferences";
import { WeatherAdjustmentChip } from "./WeatherAdjustmentChip";
import type { TripPlan } from "../lib/api";

// A cold, windy leg that pushes the arrival below the target. The server's numbers are metric: 5 degrees C, 40 km/h.
const plan = (leg: object) =>
  ({
    Routes: [{ ArrivalSoC: 30 }],
    weather_adjustment: { adjusted_arrival_soc: [], final_arrival_soc: 18, below_target: true, target_arrival_soc: 20, legs: [{ multiplier: 1.2, ...leg }] },
  }) as unknown as TripPlan;

const prefs = (distanceUnit: "mi" | "km", temperatureUnit: "c" | "f") => {
  setDistanceUnit(distanceUnit);
  setTemperatureUnit(temperatureUnit);
};

afterEach(cleanup);

describe("WeatherAdjustmentChip units", () => {
  it("says temperature and headwind in the account's imperial units", () => {
    prefs("mi", "f");
    render(<WeatherAdjustmentChip plan={plan({ temp_c: 5, headwind_kph: 40 })} />);
    expect(screen.getByText(/cold \(41°F\) \+ 25 mph headwind/)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/°C|kph|km\/h/);
  });
  it("says them in metric when the account is metric", () => {
    prefs("km", "c");
    render(<WeatherAdjustmentChip plan={plan({ temp_c: 5, headwind_kph: 40 })} />);
    expect(screen.getByText(/cold \(5°C\) \+ 40 km\/h headwind/)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/°F|mph|kph/);
  });
  it("keeps the thresholds in metric: 32 C is heat and reads 90°F; 9 C is not cold in either unit", () => {
    prefs("mi", "f");
    const { unmount } = render(<WeatherAdjustmentChip plan={plan({ temp_c: 32 })} />);
    expect(screen.getByText(/heat \(90°F\)/)).toBeTruthy();
    unmount();
    render(<WeatherAdjustmentChip plan={plan({ temp_c: 12, headwind_kph: 8 })} />);
    expect(document.body.textContent).not.toMatch(/cold|heat|headwind/);
  });
});

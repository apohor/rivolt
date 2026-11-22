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
  it("keeps the thresholds in metric: 32 C is heat and reads 90°F", () => {
    prefs("mi", "f");
    render(<WeatherAdjustmentChip plan={plan({ temp_c: 32 })} />);
    expect(screen.getByText(/heat \(90°F\)/)).toBeTruthy();
  });
  it("pins the edges: 9 C is cold and 10 C is not; 10 km/h is silent and 11 is named", () => {
    prefs("km", "c");
    const { unmount } = render(<WeatherAdjustmentChip plan={plan({ temp_c: 9, headwind_kph: 11 })} />);
    expect(screen.getByText(/cold \(9°C\) \+ 11 km\/h headwind/)).toBeTruthy();
    unmount();
    render(<WeatherAdjustmentChip plan={plan({ temp_c: 10, headwind_kph: 10, precip_mm: 1 })} />);
    expect(document.body.textContent).not.toMatch(/cold|headwind/);
  });
  it("says feels like when the air temperature on the stop row differs", () => {
    prefs("mi", "f");
    render(<WeatherAdjustmentChip plan={plan({ temp_c: 1, air_temp_c: 5 })} />);
    expect(screen.getByText(/cold \(feels like 34°F\)/)).toBeTruthy();
  });
  it("does not say feels like when both round to the same temperature", () => {
    prefs("km", "c");
    render(<WeatherAdjustmentChip plan={plan({ temp_c: 4.6, air_temp_c: 5.2 })} />);
    expect(screen.getByText(/cold \(5°C\)/)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/feels like/);
  });
});

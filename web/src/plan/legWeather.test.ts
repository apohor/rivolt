import { describe, expect, it } from "vitest";
import { climbLine, legEstimateLine, legWeatherFacts, legWeatherTags } from "./legWeather";

describe("legWeatherFacts", () => {
  it("states temperature and a headwind in the account's units", () => {
    const f = legWeatherFacts({ multiplier: 1.1, temp_c: 5, headwind_kph: 40, precip_mm: 2 }, "f", "mi");
    expect(legWeatherTags(f)).toEqual(["41 °F", "25 mph headwind", "rain"]);
    expect(f?.tempShort).toBe("41°");
    expect(legWeatherTags(legWeatherFacts({ multiplier: 1, temp_c: 5, headwind_kph: 40 }, "c", "km"))).toEqual(["5 °C", "40 km/h headwind"]);
  });
  it("calls a negative headwind a tailwind and ignores a light breeze", () => {
    expect(legWeatherTags(legWeatherFacts({ multiplier: 1, headwind_kph: -20 }, "f", "mi"))).toEqual(["12 mph tailwind"]);
    expect(legWeatherTags(legWeatherFacts({ multiplier: 1, headwind_kph: 5 }, "f", "mi"))).toEqual([]);
  });
  it("says rain only when the wet factor applied, and snow only when the server says snow", () => {
    expect(legWeatherFacts({ multiplier: 1, precip_mm: 0.5 }, "f", "mi")).toBeNull();
    expect(legWeatherFacts({ multiplier: 1.05, precip_mm: 0.6 }, "f", "mi")?.rain).toBe(true);
    const snow = legWeatherFacts({ multiplier: 1.2, temp_c: -6, precip_mm: 3, snow: true }, "f", "mi");
    expect(legWeatherTags(snow)).toEqual(["21 °F", "snow expected"]);
    expect(snow?.rain).toBe(false);
  });
  it("uses a real minus below zero and returns nothing for an empty leg", () => {
    expect(legWeatherTags(legWeatherFacts({ multiplier: 1, temp_c: -10 }, "c", "km"))).toEqual(["−10 °C"]);
    expect(legWeatherFacts({ multiplier: 1 }, "f", "mi")).toBeNull();
    expect(legWeatherFacts(undefined, "f", "mi")).toBeNull();
  });
});

describe("air temperature", () => {
  it("shows the air temperature and keeps feels-like for the details", () => {
    const f = legWeatherFacts({ multiplier: 1, temp_c: -6, air_temp_c: 0, headwind_kph: 20 }, "f", "mi");
    expect(f?.temp).toBe("32 °F");
    expect(f?.feels).toBe("21 °F");
    expect(legWeatherFacts({ multiplier: 1, temp_c: 5, air_temp_c: 5 }, "f", "mi")?.feels).toBeUndefined();
  });
});

describe("legEstimateLine", () => {
  it("is one estimate line, with or without a setup factor", () => {
    expect(legEstimateLine(1.14, true)).toBe("Weather and your setup: +14% (estimate)");
    expect(legEstimateLine(1.14, false)).toBe("Weather: +14% (estimate)");
    expect(legEstimateLine(0.92, false)).toBe("Weather: −8% (estimate)");
    expect(legEstimateLine(1.003, false)).toBeNull();
  });
});

describe("climbLine", () => {
  it("follows the account's unit and waits for the server's figure", () => {
    expect(climbLine(579, 670, "mi")).toBe("Climb +1,900 ft · descent −2,198 ft");
    expect(climbLine(579, 670, "km")).toBe("Climb +579 m · descent −670 m");
    expect(climbLine(undefined, 670, "mi")).toBeNull();
  });
});

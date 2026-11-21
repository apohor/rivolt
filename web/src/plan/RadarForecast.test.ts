import { describe, expect, it } from "vitest";
import { hasForecastTag, teaserText } from "./RadarForecast";

describe("forecast radar copy", () => {
  it("words the teaser by the count, in the product's voice", () => {
    expect(teaserText(1)).toBe("Rain or snow is likely at 1 stop on this route. The Pro tier shows which.");
    expect(teaserText(3)).toBe("Rain or snow is likely at 3 stops on this route. The Pro tier shows which.");
  });
  it("credits only when a leg carries rain or snow", () => {
    const base = { adjusted_arrival_soc: [], final_arrival_soc: 0, below_target: false, target_arrival_soc: 0 };
    expect(hasForecastTag({ ...base, legs: [{ multiplier: 1, radar: { level: "dry" } }] })).toBe(false);
    expect(hasForecastTag({ ...base, legs: [{ multiplier: 1 }] })).toBe(false);
    expect(hasForecastTag({ ...base, legs: [{ multiplier: 1 }, { multiplier: 1, radar: { level: "snow" } }] })).toBe(true);
    expect(hasForecastTag(undefined)).toBe(false);
  });
});

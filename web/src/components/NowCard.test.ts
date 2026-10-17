import { describe, expect, it } from "vitest";
import { placeLine } from "./NowCard";
import type { NowView } from "../lib/nowState";

const away = { place: "away", kmFromHome: 4.26 } as unknown as NowView;

describe("placeLine", () => {
  it("renders miles by default unit and km on request", () => {
    expect(placeLine(away, "mi")).toBe("Parked · 2.6 mi from home");
    expect(placeLine(away, "km")).toBe("Parked · 4.3 km from home");
  });
});

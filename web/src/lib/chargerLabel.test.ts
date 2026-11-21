import { describe, expect, it } from "vitest";
import { chargeFlavorOf, chargerLabel, placeOfFix as placeOf } from "./nowState";

const home = { latitude: 40, longitude: -100 };
const label = (peak: number, place: "home" | "away" | "unknown", known = true, rivian = false) => chargerLabel(chargeFlavorOf(peak, place, known), rivian);

describe("charger label (Now and Live share it)", () => {
  it("home AC is the home charger", () => expect(label(11, "home")).toBe("Home charger"));
  it("away AC is a public charger once the session's peak is known, and unsaid before", () => {
    expect(label(11, "away")).toBe("Public charger");
    expect(label(11, "away", false)).toBeNull();
  });
  it("DC away is a fast charger", () => expect(label(150, "away")).toBe("Fast charger"));
  it("DC at the home location is still a fast charger (power, not place)", () => expect(label(56, "home")).toBe("Fast charger"));
  it("the truck's Rivian flag gives its own label, DC or not", () => {
    expect(label(150, "away", true, true)).toBe("Rivian charger");
    expect(label(11, "unknown", false, true)).toBe("Rivian charger");
  });
  it("no home point or no fix: place unknown, a slow charge says nothing", () => {
    expect(placeOf(null, home)).toBe("unknown");
    expect(placeOf(home, { latitude: 0, longitude: 0 })).toBe("unknown");
    expect(label(11, placeOf(null, home))).toBeNull();
  });
  it("home is within 250 m of the home point", () => {
    expect(placeOf(home, { latitude: 40.001, longitude: -100 })).toBe("home");
    expect(placeOf(home, { latitude: 40.01, longitude: -100 })).toBe("away");
  });
});

import { beforeEach, describe, expect, it } from "vitest";
import { cardHeight, cardKind, claimCardHeights, forgetCardHeights, placeholderHeight, rememberCardHeight } from "./nowCardSize";

describe("the Now card's remembered height", () => {
  beforeEach(() => window.localStorage.clear());

  it("keeps a height per kind of card, so a parked card does not take a charging card's", () => {
    rememberCardHeight("v1", true, "charging", 827);
    rememberCardHeight("v1", true, "parked", 463);
    expect(cardHeight("v1", true, "charging")).toBe(827);
    expect(cardHeight("v1", true, "parked")).toBe(463);
    expect(cardHeight("v1", true, "driving")).toBeNull();
    expect(cardHeight("v1", false, "parked")).toBeNull();
  });

  it("sizes the first paint from the truck picked last and the card drawn last, before the vehicle list has answered", () => {
    window.localStorage.setItem("rivolt.selectedVehicleID", "v2");
    expect(placeholderHeight(undefined)).toBeNull();
    rememberCardHeight("v2", true, "charging", 700);
    rememberCardHeight("v2", true, "parked", 450);
    rememberCardHeight("v2", true, "charging", 710);
    expect(placeholderHeight(undefined)).toBe(710);
    // The list names the truck: the same card; another truck was never drawn, so it is the parked height or nothing.
    expect(placeholderHeight("v2")).toBe(710);
    expect(placeholderHeight("v3")).toBeNull();
  });

  it("ignores heights that cannot be a card and forgets everything on sign-out", () => {
    rememberCardHeight("v1", true, "parked", 20);
    rememberCardHeight("v1", true, "parked", 5000);
    expect(cardHeight("v1", true, "parked")).toBeNull();
    rememberCardHeight("v1", true, "parked", 450);
    forgetCardHeights();
    expect(cardHeight("v1", true, "parked")).toBeNull();
    expect(placeholderHeight("v1")).toBeNull();
  });

  it("counts plugged, asleep and offline as the parked kind", () => {
    expect(["charging", "driving", "parked", "plugged", "asleep", "offline"].map(cardKind)).toEqual(["charging", "driving", "parked", "parked", "parked", "parked"]);
  });

  it("forgets them for another account, and keeps them for the same one", () => {
    claimCardHeights("u1");
    rememberCardHeight("v1", true, "charging", 827);
    claimCardHeights("u1");
    expect(cardHeight("v1", true, "charging")).toBe(827);
    claimCardHeights("u2");
    expect(cardHeight("v1", true, "charging")).toBeNull();
    expect(placeholderHeight("v1")).toBeNull();
    // A store from before the owner was kept belongs to nobody we know.
    window.localStorage.clear();
    rememberCardHeight("v1", true, "charging", 827);
    claimCardHeights("u1");
    expect(cardHeight("v1", true, "charging")).toBeNull();
  });
});

import { describe, expect, it } from "vitest";
import { pickImageForNow, pickImageForState, pickVehicleImage } from "./vehicleImage";
import type { VehicleState } from "./api";

// Placement and design names as Rivian sends them; the URLs are made up.
const names = ["front", "in-use", "overhead", "rear", "side-charging", "side-rear", "side", "three-quarter"];
const images = names.flatMap((placement) =>
  ["dark", "light"].map((design) => ({ url: `https://img.example/${placement}-${design}.png`, placement, design, size: "large" })),
);

describe("pickImageForNow", () => {
  it("parked: the three-quarter view in the page's design", () => {
    expect(pickImageForNow(images, "parked", false)).toBe("https://img.example/three-quarter-dark.png");
    expect(pickImageForNow(images, "plugged", true)).toBe("https://img.example/three-quarter-light.png");
  });
  it("charging: the plugged side view; driving: the side view", () => {
    expect(pickImageForNow(images, "charging", false)).toBe("https://img.example/side-charging-dark.png");
    expect(pickImageForNow(images, "driving", true)).toBe("https://img.example/side-light.png");
  });
  it("falls back through side, then front, and to the other design", () => {
    expect(pickImageForNow(images.filter((i) => i.placement !== "three-quarter"), "parked", false)).toBe("https://img.example/side-dark.png");
    expect(pickImageForNow(images.filter((i) => i.placement === "front" && i.design === "dark"), "charging", true)).toBe("https://img.example/front-dark.png");
    expect(pickImageForNow([], "parked", false)).toBe("");
    expect(pickImageForNow(undefined, "parked", false)).toBe("");
  });
  it("never picks an overhead or in-use shot while an exterior one exists", () => {
    expect(pickVehicleImage(images.filter((i) => ["overhead", "in-use", "rear"].includes(i.placement)), [], false)).toBe("https://img.example/rear-dark.png");
  });
  it("still reads the older placement strings", () => {
    const old = [{ url: "u1", placement: "side-exterior-3qfront-driver" }, { url: "u2", placement: "interior-cabin-driver" }];
    expect(pickImageForNow(old, "parked", false)).toBe("u1");
  });
});

describe("pickImageForState (Live)", () => {
  const closed = { frunk_closed: true, liftgate_closed: true, tonneau_closed: true, doors_closed: true } as VehicleState;
  it("charging, driving, frunk open and the default", () => {
    expect(pickImageForState(images, { ...closed, charger_status: "chrgr_sts_connected_charging", charger_state: "charging_active" } as VehicleState, false)).toBe("https://img.example/side-charging-dark.png");
    expect(pickImageForState(images, { ...closed, gear: "D" } as VehicleState, false)).toBe("https://img.example/side-dark.png");
    expect(pickImageForState(images, { ...closed, frunk_closed: false } as VehicleState, true)).toBe("https://img.example/front-light.png");
    expect(pickImageForState(images, closed, false)).toBe("https://img.example/three-quarter-dark.png");
    expect(pickImageForState(images, undefined, true)).toBe("https://img.example/three-quarter-light.png");
  });
});

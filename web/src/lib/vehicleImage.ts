import { useEffect, useState } from "react";
import type { VehicleState } from "./api";

// Rivian's configurator images carry a placement (front, side, three-quarter, rear, side-charging, side-rear, overhead,
// in-use) and a design (dark or light, to sit on a dark or light page). Now and Live pick through this one copy.
export type PlacedImage = { url: string; placement?: string; design?: string; size?: string };

// Older placement strings (side-exterior-3qfront-driver, ...) fold into the same names.
function canon(placement: string | undefined): string {
  const p = (placement || "").toLowerCase();
  if (!p || p.includes("interior")) return "";
  if (p.includes("charging")) return "side-charging";
  if (p.includes("3qfront") || p === "three-quarter") return "three-quarter";
  if (p.includes("3qrear") || p === "side-rear") return "side-rear";
  if (p === "side" || p.startsWith("side-exterior")) return "side";
  if (p.startsWith("front")) return "front";
  if (p.startsWith("rear")) return "rear";
  return p;
}

// The first placement in `wants` that exists wins; within it, the design that matches the page and the large size.
export function pickVehicleImage(images: readonly PlacedImage[] | undefined, wants: readonly string[], light: boolean): string {
  if (!images || images.length === 0) return "";
  const design = light ? "light" : "dark";
  for (const want of [...wants, "three-quarter", "side", "side-rear", "front", "rear"]) {
    const hits = images.filter((i) => canon(i.placement) === want);
    if (hits.length === 0) continue;
    const rank = (i: PlacedImage) => (i.design === design ? 0 : 2) + (i.size === "large" ? 0 : 1);
    return [...hits].sort((a, b) => rank(a) - rank(b))[0].url;
  }
  return "";
}

// Live: the angle that illustrates the vehicle's state right now (the iPhone's VehiclePicture.live is the same rule: the parity table in
// ios/RivoltTests/Fixtures/vehicle_picture_parity.json holds both). A closure the truck has not reported counts as shut.
// Rivian's side-charging art is a cropped close-up of the front half, so it is never chosen: the whole truck shows while plugged in.
export function pickImageForState(images: readonly PlacedImage[] | undefined, state: VehicleState | undefined, light: boolean): string {
  const wants: string[] = [];
  if (state) {
    const driving = ["D", "R", "N"].includes((state.gear || "").toUpperCase());
    if (driving) wants.push("side", "three-quarter");
    if (state.frunk_closed === false) wants.push("front", "three-quarter");
    if (state.liftgate_closed === false || state.tonneau_closed === false) wants.push("rear", "side-rear");
    if (state.doors_closed === false) wants.push("side");
  }
  return pickVehicleImage(images, wants, light);
}

// Live: the pictures to try in order, the state's pick and then the vehicle's own image_url; one that failed to load is skipped.
export function pickLiveImage(images: readonly PlacedImage[] | undefined, state: VehicleState | undefined, light: boolean, imageUrl: string | undefined, failed: readonly string[] = []): string {
  return [pickImageForState(images, state, light), imageUrl || ""].find((u) => u && !failed.includes(u)) || "";
}

// Now: driving shows the side view, every other state the three-quarter view (the whole truck, as the iPhone; the cropped side-charging art is never used).
export function pickImageForNow(images: readonly PlacedImage[] | undefined, kind: "charging" | "driving" | string, light: boolean): string {
  return pickVehicleImage(images, kind === "driving" ? ["side"] : [], light);
}

// True while the page is in the light theme (applyTheme sets data-theme="light" on <html>).
export function useIsLight(): boolean {
  const read = () => typeof document !== "undefined" && document.documentElement.getAttribute("data-theme") === "light";
  const [light, setLight] = useState(read);
  useEffect(() => {
    const mo = new MutationObserver(() => setLight(read()));
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    setLight(read());
    return () => mo.disconnect();
  }, []);
  return light;
}

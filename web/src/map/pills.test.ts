import { describe, expect, it } from "vitest";
import type { Map as MLMap } from "maplibre-gl";
import { startPillText, pillAwayFromLine, pillBox, pillImage, pillSide, placePills, placeStopPill } from "./pills";

// A map stand-in: a 400 px wide view where longitude 0..1 maps to x 0..400 and latitude 0..1 to y 300..0.
const fakeMap = { getContainer: () => ({ clientWidth: 400 }), project: ([lon, lat]: [number, number]) => ({ x: lon * 400, y: 300 - lat * 300 }) } as unknown as MLMap;

describe("placePills", () => {
  it("puts each pill on the side of its pin with more room", () => {
    const p = placePills(fakeMap, [0.2, 0.1], [0.8, 0.9]);
    expect(p.start.anchor).toBe("left"); // pin in the left half: the pill extends right
    expect(p.start.offset[0]).toBeGreaterThan(0);
    expect(p.end.anchor).toBe("right"); // pin in the right half: the pill extends left
    expect(p.end.offset[0]).toBeLessThan(0);
  });
  it("stacks the start above its pin and the end below when both are on one side and close", () => {
    const p = placePills(fakeMap, [0.8, 0.5], [0.82, 0.49]);
    expect(p.start.anchor).toBe("bottom-right");
    expect(p.end.anchor).toBe("top-right");
  });
  it("leaves them apart when the pins are far apart on one side", () => {
    const p = placePills(fakeMap, [0.8, 0.1], [0.8, 0.9]);
    expect(p.start.anchor).toBe("right");
    expect(p.end.anchor).toBe("right");
  });
});

describe("pillImage", () => {
  it("is null without a 2D canvas (jsdom), never a throw", () => {
    expect(pillImage("Start 8:13 PM", "dark")).toBeNull();
  });
});

describe("pillSide", () => {
  it("extends a pill from the pin toward the side of the view with room", () => {
    expect(pillSide(fakeMap, [0.2, 0.5])).toEqual({ anchor: "left", offset: [16, 0] });
    expect(pillSide(fakeMap, [0.8, 0.5])).toEqual({ anchor: "right", offset: [-16, 0] });
  });
});

describe("pillAwayFromLine", () => {
  it("goes on the side away from where the line leaves the pin, and off the line's height", () => {
    // Pin mid-view, the line runs off to the right and down: the pill extends left (anchor "right") and is lifted.
    expect(pillAwayFromLine({ x: 200, y: 100 }, { x: 260, y: 160 }, 400)).toEqual({ anchor: "right", offset: [-16, -6] });
    // The line runs left and up: the pill extends right and is lowered.
    expect(pillAwayFromLine({ x: 200, y: 100 }, { x: 140, y: 40 }, 400)).toEqual({ anchor: "left", offset: [16, 6] });
  });
  it("keeps to the side with more room when the line is straight up or down, or has no neighbour", () => {
    expect(pillAwayFromLine({ x: 100, y: 100 }, { x: 102, y: 200 }, 400).anchor).toBe("left");
    expect(pillAwayFromLine({ x: 300, y: 100 }, null, 400)).toEqual({ anchor: "right", offset: [-16, 0] });
  });
  it("with no room on the away side it goes above or below the pin, along the roomy side, clear of a level line", () => {
    // The line goes right from a pin 60 px from the left edge, level: a pill left of it would run off the view, and one to the right would lie along
    // the line. It goes above the pin, extending right.
    expect(pillAwayFromLine({ x: 60, y: 100 }, { x: 120, y: 100 }, 400)).toEqual({ anchor: "bottom-left", offset: [4, -10] });
    // The mirror: the end of a west-to-east route at the right edge, the line arriving from the left.
    expect(pillAwayFromLine({ x: 360, y: 100 }, { x: 300, y: 100 }, 400)).toEqual({ anchor: "bottom-right", offset: [-4, -10] });
    // A line that leaves upward puts the pill below.
    expect(pillAwayFromLine({ x: 60, y: 100 }, { x: 120, y: 40 }, 400)).toEqual({ anchor: "top-left", offset: [4, 10] });
  });
});

describe("pillBox", () => {
  it("puts a pill's anchored corner or edge at the pin moved by the offset", () => {
    const s = { w: 100, h: 20 };
    expect(pillBox({ x: 200, y: 100 }, "left", [16, 0], s)).toEqual({ x0: 216, y0: 90, x1: 316, y1: 110 });
    expect(pillBox({ x: 200, y: 100 }, "right", [-16, 0], s)).toEqual({ x0: 84, y0: 90, x1: 184, y1: 110 });
    expect(pillBox({ x: 200, y: 100 }, "bottom-left", [4, -10], s)).toEqual({ x0: 204, y0: 70, x1: 304, y1: 90 });
    expect(pillBox({ x: 200, y: 100 }, "top-right", [-4, 10], s)).toEqual({ x0: 96, y0: 110, x1: 196, y1: 130 });
  });
});

describe("placeStopPill", () => {
  const size = { w: 110, h: 23 };
  const view = { w: 360, h: 460 };
  const pin = { x: 180, y: 230 };
  const usual = { anchor: "left", offset: [16, 0] as [number, number] };
  it("keeps its usual side when that is clear", () => {
    expect(placeStopPill(pin, size, usual, [], view)).toEqual(usual);
  });
  it("takes the other side when the usual one is taken", () => {
    // An end pill sits where the usual (right) side would be.
    const taken = [pillBox(pin, "left", [16, 0], { w: 100, h: 23 })];
    expect(placeStopPill(pin, size, usual, taken, view)).toEqual({ anchor: "right", offset: [-16, 0] });
  });
  it("goes below, then above, the pin when both sides are taken (a level line with the end pills above it)", () => {
    const taken = [pillBox(pin, "left", [16, 0], { w: 100, h: 23 }), pillBox(pin, "right", [-16, 0], { w: 100, h: 23 })];
    expect(placeStopPill(pin, size, usual, taken, view)).toEqual({ anchor: "top-left", offset: [4, 18] });
    const withBelow = [...taken, pillBox(pin, "top-left", [4, 12], size)];
    expect(placeStopPill(pin, size, usual, withBelow, view)).toEqual({ anchor: "bottom-left", offset: [4, -18] });
  });
  it("is dropped (null) when no place clears everything or stays in the view", () => {
    const everywhere = [{ x0: 0, y0: 0, x1: 360, y1: 460 }];
    expect(placeStopPill(pin, size, usual, everywhere, view)).toBeNull();
    // A pin at the right edge: the usual side (right) runs out of the view, and the left one is used.
    expect(placeStopPill({ x: 350, y: 230 }, size, usual, [], view)).toEqual({ anchor: "right", offset: [-16, 0] });
  });
});

describe("startPillText", () => {
  it("says both times on a loop (one pin), the start alone otherwise", () => {
    expect(startPillText(true, "9:23 PM", "11:28 PM")).toBe("Start 9:23 PM · End 11:28 PM");
    expect(startPillText(false, "9:23 PM", "11:28 PM")).toBe("Start 9:23 PM");
    expect(startPillText(true, "9:23 PM", "")).toBe("Start 9:23 PM");
  });
});

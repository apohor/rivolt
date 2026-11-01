import { describe, expect, it } from "vitest";
import type { Map as MLMap } from "maplibre-gl";
import { pillAwayFromLine, pillImage, pillSide, placePills } from "./pills";

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
  it("falls back to the roomy side when the away side has no room for a pill", () => {
    // The line goes right from a pin 60 px from the left edge: a pill left of it would run off the view.
    expect(pillAwayFromLine({ x: 60, y: 100 }, { x: 120, y: 100 }, 400).anchor).toBe("left");
  });
});

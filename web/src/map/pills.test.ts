import { describe, expect, it } from "vitest";
import type { Map as MLMap } from "maplibre-gl";
import { pillImage, placePills } from "./pills";

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

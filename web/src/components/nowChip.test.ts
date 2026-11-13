import { describe, expect, it } from "vitest";
import { nowChip } from "./NowHeroV2";
import type { NowView } from "../lib/nowState";

const view = (o: Partial<NowView>) => ({ kind: "parked", stale: false, stateSinceMs: null, ...o }) as NowView;

describe("nowChip", () => {
  it("never reads Live: a parked truck with no state time says Parked", () => {
    expect(nowChip(view({}), null, 0, "Live")).toBe("Parked");
    expect(nowChip(view({ kind: "plugged" }), null, 0, "Live")).toBe("Plugged in");
    expect(nowChip(view({ kind: "asleep" }), null, 0, "Live")).toBe("Asleep");
  });
  it("keeps a stale or offline pill's own words", () => {
    expect(nowChip(view({ kind: "offline" }), null, 0, "Last seen 3h ago")).toBe("Last seen 3h ago");
  });
});

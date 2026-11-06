import { describe, expect, it } from "vitest";
import { movableVia, moveVia } from "./reorderVias";

describe("reorder vias", () => {
  it("swaps with the next via, skipping an overnight", () => {
    const r = moveVia(["a", "n", "b"], [false, true, false], 0, 1);
    expect(r.stops).toEqual(["b", "n", "a"]);
    expect(r.flags).toEqual([false, true, false]);
  });
  it("does nothing at the ends or on an overnight", () => {
    expect(moveVia(["a", "b"], [false, false], 0, -1).stops).toEqual(["a", "b"]);
    expect(moveVia(["a", "b"], [false, false], 1, 1).stops).toEqual(["a", "b"]);
    expect(moveVia(["a", "n"], [false, true], 1, -1).stops).toEqual(["a", "n"]);
    expect(movableVia([false, true], 0, 1)).toBe(-1);
  });
});

import { describe, expect, it } from "vitest";
import { movableVia, moveVia, viaMoveLabel, viaPosition, viaRemoveLabel } from "./reorderVias";

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

describe("accessible names for the Via rows", () => {
  it("counts only the person's own stops, so two pins differ", () => {
    const flags = [false, true, false, false];
    expect(viaPosition(flags, 0)).toEqual({ n: 1, total: 3 });
    expect(viaPosition(flags, 2)).toEqual({ n: 2, total: 3 });
    expect(viaPosition(flags, 3)).toEqual({ n: 3, total: 3 });
    const a = viaMoveLabel("A place you chose", 1, 2, 1);
    const b = viaMoveLabel("A place you chose", 2, 2, 1);
    expect(a).not.toBe(b);
    expect(a).toBe("Move stop 1 of 2, A place you chose, later");
    expect(viaMoveLabel("X", 2, 2, -1)).toBe("Move stop 2 of 2, X, earlier");
  });
  it("the remove name starts with the visible word", () => {
    expect(viaRemoveLabel("A place you chose", 2)).toBe("remove stop 2: A place you chose");
  });
});

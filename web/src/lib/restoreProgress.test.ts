import { describe, expect, it } from "vitest";
import { restorePartialNote, restoreSoFar } from "./restoreProgress";

describe("restore progress text", () => {
  it("says nothing before the first counted event (the start event has no counts)", () => {
    expect(restoreSoFar(null)).toBe("");
    expect(restoreSoFar({ event: "start" })).toBe("");
    expect(restoreSoFar({ event: "start" })).not.toContain("undefined");
  });

  it("shows the counts once they arrive, including zeros", () => {
    expect(restoreSoFar({ event: "progress", drives: 0, charges: 2, samples: 4000 })).toBe(
      " 0 drives, 2 charges, 4000 samples so far.",
    );
  });

  it("adds the partly-restored note only when rows were written", () => {
    expect(restorePartialNote(null)).toBe("");
    expect(restorePartialNote({ event: "start" })).toBe("");
    expect(restorePartialNote({ event: "progress", drives: 0, charges: 0, samples: 0 })).toBe("");
    expect(restorePartialNote({ event: "progress", drives: 1, charges: 0, samples: 0 })).toContain("partly restored");
    expect(restorePartialNote({ event: "progress", drives: 0, charges: 0, samples: 2000 })).toContain("2000 samples");
  });
});

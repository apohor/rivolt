import { describe, expect, it } from "vitest";
import { cleanTrack } from "./driveTrack";

// Plainly fake places near (10, 10), one degree-thousandth per step along a line.
const T0 = Date.parse("2026-11-17T12:00:00Z");
const iso = (secs: number) => new Date(T0 + secs * 1000).toISOString();
type S = { At: string; Lat: number; Lon: number; LocationFixAt?: string; id: string };
const s = (id: string, at: number, step: number, fix?: number): S => ({
  id,
  At: iso(at),
  Lat: 10 + step * 0.001,
  Lon: 10 + step * 0.001,
  ...(fix === undefined ? {} : { LocationFixAt: iso(fix) }),
});
const ids = (xs: S[]) => xs.map((x) => x.id).join(",");

describe("cleanTrack", () => {
  it("keeps a clean track as it is", () => {
    const xs = [s("a", 0, 0, 0), s("b", 5, 1, 5), s("c", 10, 2, 10)];
    expect(ids(cleanTrack(xs))).toBe("a,b,c");
  });

  it("drops a fix older than the newest one already kept (the line no longer jumps back)", () => {
    // d arrives after c but carries b's older fix: drawn in arrival order the line would go 0,1,2,(back to 1),3.
    const xs = [s("a", 0, 0, 0), s("b", 5, 1, 5), s("c", 10, 2, 10), s("d", 15, 1, 5), s("e", 20, 3, 20)];
    expect(ids(cleanTrack(xs))).toBe("a,b,c,e");
  });

  it("drops a sample that repeats the previous fix", () => {
    const xs = [s("a", 0, 0, 0), s("b", 5, 1, 5), s("b2", 10, 1, 5), s("b3", 15, 1, 5), s("c", 20, 2, 20)];
    expect(ids(cleanTrack(xs))).toBe("a,b,c");
  });

  it("keeps the kept positions strictly forward in fix time, whatever the arrival order of the rest", () => {
    // Interleaved late fixes, as the modem sends them: every fix is stamped before the frame that carries it arrives.
    const xs = [s("a", 0, 0, 0), s("b", 30, 4, 20), s("c", 31, 1, 5), s("d", 32, 2, 10), s("e", 40, 5, 25), s("f", 41, 3, 15)];
    const out = cleanTrack(xs);
    expect(ids(out)).toBe("a,b,e");
    const fixes = out.map((x) => Date.parse(x.LocationFixAt as string));
    expect(fixes).toEqual([...fixes].sort((p, q) => p - q));
  });

  it("takes a sample with no fix time to be fixed when it arrived", () => {
    const xs = [s("a", 0, 0), s("b", 5, 1), s("c", 10, 2, 10), s("d", 15, 3)];
    expect(ids(cleanTrack(xs))).toBe("a,b,c,d");
  });

  it("a fix stamped after its own frame is not believed, so one bad stamp does not hide the rest", () => {
    // b claims a fix an hour after it arrived. Believed, every later sample would be "older" and vanish.
    const xs = [s("a", 0, 0, 0), s("b", 5, 1, 3600), s("c", 10, 2, 10), s("d", 15, 3, 15)];
    expect(ids(cleanTrack(xs))).toBe("a,b,c,d");
  });

  it("leaves samples with no position where they are, and does not let them move the newest fix", () => {
    const noPos = { id: "n", At: iso(7), Lat: 0, Lon: 0, LocationFixAt: iso(7) } as S;
    const xs = [s("a", 0, 0, 0), s("b", 5, 1, 5), noPos, s("c", 10, 2, 10)];
    expect(ids(cleanTrack(xs))).toBe("a,b,n,c");
  });

  it("sorts by arrival first, so an unsorted input gives the same answer", () => {
    const xs = [s("c", 10, 2, 10), s("a", 0, 0, 0), s("d", 15, 1, 5), s("b", 5, 1, 5)];
    expect(ids(cleanTrack(xs))).toBe("a,b,c");
  });

  it("does not change its input", () => {
    const xs = [s("a", 0, 0, 0), s("a2", 5, 0, 0)];
    cleanTrack(xs);
    expect(ids(xs)).toBe("a,a2");
  });

  it("is empty for nothing, and keeps a single sample", () => {
    expect(cleanTrack([])).toEqual([]);
    expect(ids(cleanTrack([s("a", 0, 0, 0)]))).toBe("a");
  });

  it("a stale replay of one fix (every sample the same fix) leaves the first sample", () => {
    const xs = [s("a", 0, 0, 0), s("b", 5, 0, 0), s("c", 10, 0, 0)];
    expect(ids(cleanTrack(xs))).toBe("a");
  });
});

import { afterEach, describe, expect, it } from "vitest";
import { probe, resetWebGLProbe, webglAvailable } from "./webgl";

const docWith = (getContext: (kind: string) => unknown) =>
  ({ createElement: () => ({ getContext }) }) as unknown as Pick<Document, "createElement">;

describe("WebGL detection", () => {
  afterEach(() => resetWebGLProbe());

  it("is available when a context can be made, and the probe context is released", () => {
    let lost = false;
    const gl = { getExtension: () => ({ loseContext: () => { lost = true; } }) };
    expect(probe(docWith(() => gl))).toBe(true);
    expect(lost).toBe(true);
  });

  it("prefers webgl2 and falls back to webgl", () => {
    const asked: string[] = [];
    expect(probe(docWith((k) => { asked.push(k); return k === "webgl" ? { getExtension: () => null } : null; }))).toBe(true);
    expect(asked).toEqual(["webgl2", "webgl"]);
  });

  it("is unavailable when no context can be made", () => {
    expect(probe(docWith(() => null))).toBe(false);
  });

  it("is unavailable when creating a context throws (a privacy browser or a blocked GPU)", () => {
    expect(probe(docWith(() => { throw new Error("blocked"); }))).toBe(false);
  });

  it("caches the answer", () => {
    let calls = 0;
    const doc = docWith(() => { calls++; return { getExtension: () => null }; });
    expect(webglAvailable(doc)).toBe(true);
    expect(webglAvailable(doc)).toBe(true);
    expect(calls).toBe(1);
  });
});

import { describe, expect, it } from "vitest";
// @ts-expect-error: a build-time script written as plain ESM
import { findFloorApis } from "../../scripts/check-floor-apis.mjs";

describe("the post-floor API scan (the build ships no polyfills)", () => {
  it("passes code that stays inside the supported browsers", () => {
    const ok = "const a=[1,2].flatMap(x=>[x]);const b=[3,1].toSorted();const m=new Map();for(const v of m.values())v;Array.from(m.values()).map(x=>x);structuredClone({});";
    expect(findFloorApis(ok)).toEqual([]);
  });
  it("does not mistake a library's own method of the same name for the new built-in", () => {
    const own = "class F{constructor(){}difference(e){return this.r===e.r?`none`:`rtt`}}const d=a?.difference(o);";
    expect(findFloorApis(own)).toEqual([]);
    expect(findFloorApis(own + "new Set(a).union(b)")).toContain("Set methods");
    expect(findFloorApis("class F{difference(e){return 1}}new Set(a).difference(b)")).toEqual([]); // documented blind spot: same name defined and used
  });
  it.each([
    ["Object.groupBy(a,f)", "Object.groupBy / Map.groupBy"],
    ["const {promise}=Promise.withResolvers()", "Promise.withResolvers"],
    ["new Set(a).union(new Set(b))", "Set methods"],
    ["m.values().map(x=>x)", "Iterator helpers"],
    ["s.matchAll(/a/g).filter(Boolean)", "Iterator helpers"],
    ["Iterator.from(x)", "Iterator helpers"],
    ["URL.canParse(u)", "URL.canParse"],
    ["URL.parse(u)", "URL.parse"],
    ["Promise.try(f)", "Promise.try"],
    ["buf.transfer(8)", "ArrayBuffer.prototype.transfer"],
    ["new Uint8Array(4).toBase64()", "Uint8Array base64 / hex"],
  ])("flags %s", (code, name) => {
    expect(findFloorApis(code)).toContain(name);
  });
});
